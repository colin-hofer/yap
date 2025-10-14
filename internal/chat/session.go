package chat

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"

	"yap/internal/config"
	"yap/internal/membership"
)

// Options describe how to initialise a chat session.
type Options struct {
	Config  config.Config
	Listen  func(string) (net.PacketConn, error)
	Resolve func(string) (net.Addr, error)
	Cipher  Cipher
	Store   config.Store
}

// Chat manages the gossip loop, user interaction, and graceful shutdown.
type Chat struct {
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
	members      *membership.Manager
	addrMu       sync.RWMutex
	addresses    map[string]net.Addr
	resolve      func(string) (net.Addr, error)
}

// NewChat creates a new chat session.
func NewChat(opts Options) (*Chat, error) {
	cfg := config.Normalize(opts.Config)

	listen := opts.Listen
	if listen == nil {
		listen = func(addr string) (net.PacketConn, error) {
			return net.ListenPacket("udp", addr)
		}
	}

	resolve := opts.Resolve
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

	session := &Chat{
		cfg:       cfg,
		bootstrap: make([]net.Addr, 0, len(cfg.Peers)),
		store:     opts.Store,
		transport: newTransport(cfg.Name, conn, opts.Cipher),
		closed:    make(chan struct{}),
		events:    make(chan Message, 128),
		members:   membership.New(localAddr, cfg.Name),
		addresses: make(map[string]net.Addr),
		resolve:   resolve,
	}

	for _, seed := range cfg.Peers {
		addr, err := session.resolve(seed)
		if err != nil {
			session.transport.Close()
			return nil, fmt.Errorf("resolve peer %q: %w", seed, err)
		}
		session.bootstrap = append(session.bootstrap, addr)
		session.markPending(addr)
	}

	session.emit(Message{Type: systemMsg, Body: fmt.Sprintf("listening on %s as %s", session.transport.LocalAddr(), cfg.Name)})
	if len(cfg.Peers) == 0 {
		session.emit(Message{Type: systemMsg, Body: "no peers provided, waiting for someone to connect"})
	}
	if session.transport.EncryptionEnabled() {
		session.emit(Message{Type: systemMsg, Body: "encryption enabled"})
	}
	session.recordEvent("session ready")
	return session, nil
}

// Events returns the events channel.
func (c *Chat) Events() <-chan Message {
	return c.events
}

// Start starts the chat application - it is idempotent.
func (c *Chat) Start() {
	c.startOnce.Do(func() {
		c.transport.Listen(c.closed, c.handleIncoming, c.handleAuthReject, c.emitSystem)
		sentDirect := false
		joinPayload := c.buildJoinPayload()
		for _, addr := range c.bootstrap {
			c.markPending(addr)
			if err := c.sendDirect(addr, joinMsg, joinPayload); err != nil {
				c.emitSystem("bootstrap to %s failed: %v", addr, err)
				_ = c.dropPeer(addr, fmt.Sprintf("failed: %v", err))
				continue
			}
			c.markActive(addr, "")
			sentDirect = true
		}
		if !sentDirect {
			if err := c.broadcast(joinMsg, joinPayload); err != nil {
				c.emitSystem("failed to announce presence: %v", err)
			}
		}
	})
}

// Submit submits a message to the chat.
func (c *Chat) Submit(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	c.Start()
	err := c.handleInput(text)
	if errors.Is(err, ErrQuit) {
		_ = c.Shutdown()
	}
	return err
}

// Shutdown shuts down the chat application.
func (c *Chat) Shutdown() error {
	var closeErr error
	c.shutdownOnce.Do(func() {
		if err := c.broadcast(leaveMsg, ""); err != nil {
			c.emitSystem("failed to send leave notice: %v", err)
		}
		closeErr = c.Close()
		close(c.events)
	})
	return closeErr
}

// Close closes the chat connection.
func (c *Chat) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return c.transport.Close()
}

func (c *Chat) handleIncoming(msg Message, addr net.Addr, raw []byte, authenticated bool) {
	suppressEmit := false
	activated := false

	switch msg.Type {
	case peersMsg:
		c.handlePeersPayload(msg.Body, addr)
		return
	case joinMsg:
		payload := strings.TrimSpace(msg.Body)
		if c.members != nil && payload != "" {
			response, additional, err := c.members.HandleJoin([]byte(payload), addr.String(), msg.From)
			if err == nil {
				if len(response) > 0 {
					if err := c.sendDirect(addr, peersMsg, string(response)); err != nil {
						c.emitSystem("failed to share peers with %s: %v", addr, err)
					}
				}
				for _, target := range additional {
					c.contactPeer(target)
				}
			}
		}
		if payload != "" {
			suppressEmit = true
		}
	}

	if msg.Type == errorMsg {
		_ = c.dropPeer(addr, msg.Body)
		c.emit(msg)
		return
	}

	if authenticated {
		if msg.Type == leaveMsg && msg.From != "" {
			_ = c.dropPeer(addr, "left the chat")
		} else {
			activated = c.markActive(addr, msg.From)
		}
	}

	if msg.Type == joinMsg && activated {
		joinCopy := msg
		joinCopy.Body = ""
		joinCopy.Cipher = ""
		joinCopy.Nonce = ""
		c.emit(joinCopy)
		suppressEmit = true
	}

	if !suppressEmit {
		c.emit(msg)
	}
	c.forwardRaw(raw, addr)
}

func (c *Chat) handleAuthReject(msg Message, addr net.Addr) {
	c.emit(msg)
	_ = c.dropPeer(addr, msg.Body)
}

func (c *Chat) buildJoinPayload() string {
	if c.members == nil {
		return ""
	}
	data, err := c.members.BuildJoinPayload()
	if err != nil {
		return ""
	}
	return string(data)
}

func (c *Chat) contactPeer(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	if c.members != nil {
		if c.members.IsLocal(addr) || c.members.Has(addr) {
			return
		}
		c.members.AddPending(addr)
	}
	resolved, err := c.resolveAddr(addr)
	if err != nil {
		c.emitSystem("peer hint %s failed: %v", addr, err)
		return
	}
	if c.members != nil && c.members.IsLocal(resolved.String()) {
		return
	}
	if c.hasAddress(resolved) {
		return
	}
	joinPayload := c.buildJoinPayload()
	c.markPending(resolved)
	c.rememberAddr(resolved)
	if err := c.sendDirect(resolved, joinMsg, joinPayload); err != nil {
		c.emitSystem("failed to reach %s: %v", resolved, err)
		_ = c.dropPeer(resolved, fmt.Sprintf("failed: %v", err))
	}
}

func (c *Chat) handlePeersPayload(body string, source net.Addr) {
	if c.members == nil || strings.TrimSpace(body) == "" {
		return
	}
	addrStr := ""
	if source != nil {
		addrStr = source.String()
	}
	additional, err := c.members.HandlePeers([]byte(body), addrStr)
	if err != nil {
		return
	}
	for _, target := range additional {
		c.contactPeer(target)
	}
}

func (c *Chat) resolveAddr(raw string) (net.Addr, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return nil, fmt.Errorf("address cannot be empty")
	}
	if c.resolve != nil {
		return c.resolve(target)
	}
	return net.ResolveUDPAddr("udp", target)
}

func (c *Chat) sendDirect(addr net.Addr, kind msgType, body string) error {
	_, raw, err := c.transport.prepare(c.cfg.Name, kind, body)
	if err != nil {
		return err
	}
	return c.transport.sendRaw(addr, raw)
}

func (c *Chat) broadcast(kind msgType, body string) error {
	msg, raw, err := c.transport.prepare(c.cfg.Name, kind, body)
	if err != nil {
		return err
	}

	if kind == chatMsg {
		local := msg
		local.Body = body
		local.Cipher = ""
		local.Nonce = ""
		c.emit(local)
	}

	c.forwardRaw(raw, nil)
	return nil
}

func (c *Chat) forwardRaw(data []byte, exclude net.Addr) {
	excludeKey := canonicalNetAddr(exclude)
	c.addrMu.RLock()
	defer c.addrMu.RUnlock()
	for key, addr := range c.addresses {
		if excludeKey != "" && key == excludeKey {
			continue
		}
		if err := c.transport.sendRaw(addr, data); err != nil {
			c.emitSystem("send to %s failed: %v", key, err)
		}
	}
}

func (c *Chat) rememberAddr(addr net.Addr) (string, bool) {
	key := canonicalNetAddr(addr)
	if key == "" {
		return "", false
	}
	c.addrMu.Lock()
	_, existed := c.addresses[key]
	c.addresses[key] = addr
	c.addrMu.Unlock()
	return key, !existed
}

func (c *Chat) forgetAddr(addr net.Addr) bool {
	key := canonicalNetAddr(addr)
	if key == "" {
		return false
	}
	c.addrMu.Lock()
	_, existed := c.addresses[key]
	if existed {
		delete(c.addresses, key)
	}
	c.addrMu.Unlock()
	return existed
}

func (c *Chat) addressKeys() []string {
	c.addrMu.RLock()
	defer c.addrMu.RUnlock()
	keys := make([]string, 0, len(c.addresses))
	for key := range c.addresses {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func canonicalNetAddr(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return canonicalAddrString(addr.String())
}

func (c *Chat) hasAddress(addr net.Addr) bool {
	key := canonicalNetAddr(addr)
	if key == "" {
		return false
	}
	c.addrMu.RLock()
	defer c.addrMu.RUnlock()
	_, ok := c.addresses[key]
	return ok
}

func canonicalAddrString(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		return ap.String()
	}
	return addr
}
