package chat

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
)

// Options describe how to initialise a chat session.
type Options struct {
	Name    string
	Listen  string
	Secret  string
	Peers   []string
	Network Network
	Cipher  Cipher
	Config  ConfigStore
}

// Chat manages the gossip loop, user interaction, and graceful shutdown.
type Chat struct {
	name         string
	listenAddr   string
	secret       string
	conn         PacketConn
	network      Network
	peers        *PeerManager
	bootstrap    []net.Addr
	seen         sync.Map
	cipher       Cipher
	config       ConfigStore
	closed       chan struct{}
	shutdownOnce sync.Once
	startOnce    sync.Once
	events       chan Message
	statusMu     sync.RWMutex
	pending      map[string]net.Addr
	lastEvent    string
}

// NewChat creates a new chat session.
func NewChat(opts Options) (*Chat, error) {
	if opts.Network == nil {
		return nil, errors.New("network is required")
	}

	conn, err := opts.Network.Listen(opts.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen on %q: %w", opts.Listen, err)
	}

	session := &Chat{
		name:       opts.Name,
		listenAddr: opts.Listen,
		secret:     opts.Secret,
		conn:       conn,
		network:    opts.Network,
		peers:      newPeerManager(),
		bootstrap:  make([]net.Addr, 0, len(opts.Peers)),
		cipher:     opts.Cipher,
		config:     opts.Config,
		closed:     make(chan struct{}),
		events:     make(chan Message, 128),
		pending:    make(map[string]net.Addr),
	}

	for _, seed := range opts.Peers {
		addr, err := opts.Network.Resolve(seed)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("resolve peer %q: %w", seed, err)
		}
		session.bootstrap = append(session.bootstrap, addr)
		session.markPending(addr)
	}

	session.emit(Message{Type: systemMsg, Body: fmt.Sprintf("listening on %s as %s", conn.LocalAddr(), opts.Name)})
	if len(opts.Peers) == 0 {
		session.emit(Message{Type: systemMsg, Body: "no peers provided, waiting for someone to connect"})
	}
	if opts.Cipher != nil {
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
		go c.listen()
		sentDirect := false
		for _, addr := range c.bootstrap {
			if err := c.sendDirect(addr, joinMsg, ""); err != nil {
				c.emitSystem("bootstrap to %s failed: %v", addr, err)
				_ = c.dropPeer(addr, fmt.Sprintf("failed: %v", err))
				continue
			}
			c.markActive(addr)
			sentDirect = true
		}
		if !sentDirect {
			if err := c.broadcast(joinMsg, ""); err != nil {
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
	return c.conn.Close()
}
