package chat

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"yap/internal/config"
)

// sessionOptions describe how to initialise a chat session.
type sessionOptions struct {
	config  config.Config
	listen  func(string) (net.PacketConn, error)
	resolve func(string) (net.Addr, error)
	cipher  packetCipher
	store   config.Store
}

// session manages the gossip loop, user interaction, and graceful shutdown.
type session struct {
	cfg          config.Config
	bootstrap    []net.Addr
	store        config.Store
	transport    *transport
	closed       chan struct{}
	shutdownOnce sync.Once
	startOnce    sync.Once
	events       chan Message
	statusMu     sync.RWMutex
	lastEvent    string
	membersMu    sync.RWMutex
	members      map[string]*member
	localAddr    string
	localIP      netip.Addr
	localPort    uint16
	resolve      func(string) (net.Addr, error)
}

// newSession creates a new chat session.
func newSession(opts sessionOptions) (*session, error) {
	cfg := config.Normalize(opts.config)

	listen := opts.listen
	if listen == nil {
		listen = func(addr string) (net.PacketConn, error) {
			return net.ListenPacket("udp", addr)
		}
	}

	resolve := opts.resolve
	if resolve == nil {
		resolve = func(target string) (net.Addr, error) {
			return net.ResolveUDPAddr("udp", target)
		}
	}

	conn, err := listen(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen on %q: %w", cfg.Listen, err)
	}

	localAddr := ""
	if conn.LocalAddr() != nil {
		localAddr = conn.LocalAddr().String()
	}

	session := &session{
		cfg:       cfg,
		bootstrap: make([]net.Addr, 0, len(cfg.Peers)),
		store:     opts.store,
		transport: newTransport(cfg.Name, conn, opts.cipher),
		closed:    make(chan struct{}),
		events:    make(chan Message, 128),
		resolve:   resolve,
	}

	session.resetMembership(localAddr)
	session.emit(Message{Type: systemMsg, Body: startupLogo})
	for _, seed := range cfg.Peers {
		addr, err := session.resolve(seed)
		if err != nil {
			session.transport.close()
			return nil, fmt.Errorf("resolve peer %q: %w", seed, err)
		}
		session.bootstrap = append(session.bootstrap, addr)
		session.markPending(addr)
	}

	session.emit(Message{Type: systemMsg, Body: fmt.Sprintf("listening on %s as %s", session.transport.localAddr(), cfg.Name)})
	if len(cfg.Peers) == 0 {
		session.emit(Message{Type: systemMsg, Body: "no peers provided, waiting for someone to connect"})
	}
	if session.transport.encryptionEnabled() {
		session.emit(Message{Type: systemMsg, Body: "encryption enabled"})
	}
	session.recordEvent("session ready")
	return session, nil
}

// eventStream returns the events channel.
func (s *session) eventStream() <-chan Message {
	return s.events
}

// Start starts the chat application - it is idempotent.
func (s *session) start() {
	s.startOnce.Do(func() {
		s.transport.listen(s.closed, s.handleIncoming, s.handleAuthReject, s.emitSystem)
		sentDirect := false
		joinPayload := s.buildJoinPayload()
		for _, addr := range s.bootstrap {
			s.markPending(addr)
			if err := s.sendDirect(addr, joinMsg, joinPayload); err != nil {
				s.emitSystem("bootstrap to %s failed: %v", addr, err)
				_ = s.dropPeer(addr, fmt.Sprintf("failed: %v", err))
				continue
			}
			s.markActive(addr, "")
			sentDirect = true
		}
		if !sentDirect {
			if err := s.broadcast(joinMsg, joinPayload); err != nil {
				s.emitSystem("failed to announce presence: %v", err)
			}
		}
	})
}

// Submit submits a message to the chat.
func (s *session) submit(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	s.start()
	err := s.handleInput(text)
	if errors.Is(err, errQuit) {
		_ = s.shutdown()
	}
	return err
}

// Shutdown shuts down the chat application.
func (s *session) shutdown() error {
	var closeErr error
	s.shutdownOnce.Do(func() {
		if err := s.broadcast(leaveMsg, ""); err != nil {
			s.emitSystem("failed to send leave notice: %v", err)
		}
		closeErr = s.close()
		close(s.events)
	})
	return closeErr
}

// Close closes the chat connection.
func (s *session) close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return s.transport.close()
}

// handleIncoming processes inbound messages, updating membership and gossiping them.
func (s *session) handleIncoming(msg Message, addr net.Addr, raw []byte, authenticated bool) {
	suppressEmit := false
	activated := false

	switch msg.Type {
	case peersMsg:
		s.handlePeersPayload(msg.Body, addr)
		return
	case joinMsg:
		payload := strings.TrimSpace(msg.Body)
		if payload != "" {
			response, additional, err := s.processJoinPayload([]byte(payload), addr.String(), msg.From)
			if err == nil {
				if len(response) > 0 {
					if err := s.sendDirect(addr, peersMsg, string(response)); err != nil {
						s.emitSystem("failed to share peers with %s: %v", addr, err)
					}
				}
				for _, target := range additional {
					s.contactPeer(target)
				}
			}
		}
		if payload != "" {
			suppressEmit = true
		}
	}

	if msg.Type == errorMsg {
		_ = s.dropPeer(addr, msg.Body)
		s.emit(msg)
		return
	}

	if authenticated {
		if msg.Type == leaveMsg && msg.From != "" {
			_ = s.dropPeer(addr, "left the chat")
		} else {
			activated = s.markActive(addr, msg.From)
		}
	}

	if msg.Type == joinMsg && activated {
		joinCopy := msg
		joinCopy.Body = ""
		joinCopy.Cipher = ""
		joinCopy.Nonce = ""
		s.emit(joinCopy)
		suppressEmit = true
	}

	if !suppressEmit {
		s.emit(msg)
	}
	s.forwardRaw(raw, addr)
}

// handleAuthReject notes authentication failures and drops the peer.
func (s *session) handleAuthReject(msg Message, addr net.Addr) {
	s.emit(msg)
	_ = s.dropPeer(addr, msg.Body)
}

// buildJoinPayload returns the serialized join envelope for this session.
func (s *session) buildJoinPayload() string {
	data, err := s.buildJoinPayloadData()
	if err != nil || len(data) == 0 {
		return ""
	}
	return string(data)
}

// contactPeer attempts to reach a hinted peer, updating membership state.
func (s *session) contactPeer(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	if s.isLocal(addr) || s.hasMember(addr) {
		return
	}
	s.addPendingMember(addr)
	resolved, err := s.resolveAddr(addr)
	if err != nil {
		s.emitSystem("peer hint %s failed: %v", addr, err)
		return
	}
	if s.isLocal(resolved.String()) {
		return
	}
	if ap, ok := addrPortFromNet(resolved); ok {
		if key := canonicalNetAddr(resolved); key != "" {
			s.setMemberEndpoint(key, ap)
		}
	}
	joinPayload := s.buildJoinPayload()
	s.markPending(resolved)
	if err := s.sendDirect(resolved, joinMsg, joinPayload); err != nil {
		s.emitSystem("failed to reach %s: %v", resolved, err)
		_ = s.dropPeer(resolved, fmt.Sprintf("failed: %v", err))
	}
}

// handlePeersPayload merges received peer hints and dials any new addresses.
func (s *session) handlePeersPayload(body string, source net.Addr) {
	if strings.TrimSpace(body) == "" {
		return
	}
	addrStr := ""
	if source != nil {
		addrStr = source.String()
	}
	additional, err := s.processPeersPayload([]byte(body), addrStr)
	if err != nil {
		return
	}
	for _, target := range additional {
		s.contactPeer(target)
	}
}

// resolveAddr normalises a textual address via the configured resolver.
func (s *session) resolveAddr(raw string) (net.Addr, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return nil, fmt.Errorf("address cannot be empty")
	}
	if s.resolve != nil {
		return s.resolve(target)
	}
	return net.ResolveUDPAddr("udp", target)
}

// sendDirect encrypts and delivers a message directly to a peer.
func (s *session) sendDirect(addr net.Addr, kind msgType, body string) error {
	_, raw, err := s.transport.prepare(s.cfg.Name, kind, body)
	if err != nil {
		return err
	}
	return s.transport.sendRaw(addr, raw)
}

// broadcast gossips an encoded message to every known peer.
func (s *session) broadcast(kind msgType, body string) error {
	msg, raw, err := s.transport.prepare(s.cfg.Name, kind, body)
	if err != nil {
		return err
	}

	if kind == chatMsg {
		local := msg
		local.Body = body
		local.Cipher = ""
		local.Nonce = ""
		s.emit(local)
	}

	s.forwardRaw(raw, nil)
	return nil
}

// forwardRaw rebroadcasts an already encoded packet to active peers.
func (s *session) forwardRaw(data []byte, exclude net.Addr) {
	excludeKey := canonicalNetAddr(exclude)
	for _, target := range s.activeEndpoints(excludeKey) {
		udp := net.UDPAddrFromAddrPort(target.ap)
		if udp == nil {
			continue
		}
		if err := s.transport.sendRaw(udp, data); err != nil {
			s.emitSystem("send to %s failed: %v", target.key, err)
		}
	}
}
