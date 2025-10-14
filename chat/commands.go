package chat

import (
	"fmt"
	"net"
	"strings"
)

func (c *Chat) handleInput(text string) error {
	switch {
	case text == "":
		return nil
	case strings.HasPrefix(text, "/"):
		return c.handleCommand(text)
	default:
		return c.broadcast(chatMsg, text)
	}
}

func (c *Chat) handleCommand(cmd string) error {
	switch {
	case cmd == "/peers":
		c.emitSystem("%s", c.describePeers())
		return nil
	case cmd == "/quit" || cmd == "/exit" || cmd == "/q":
		c.emitSystem("goodbye")
		return ErrQuit
	case strings.HasPrefix(cmd, "/group"):
		parts := strings.Fields(cmd)
		if len(parts) != 2 {
			c.emitSystem("usage: /group <name>")
			return nil
		}
		if c.config == nil {
			c.emitSystem("config saving is not available")
			return nil
		}
		groupName := parts[1]
		snapshot := Config{
			Name:   c.name,
			Listen: c.listenAddr,
			Secret: c.secret,
			Peers:  mergePeers(c.peers.Snapshot(), c.pendingSnapshot()),
		}
		if err := c.config.Save(groupName, snapshot); err != nil {
			c.emitSystem("failed to save config: %v", err)
		} else {
			c.emitSystem("saved config %q with %d peers", groupName, len(snapshot.Peers))
		}
		return nil
	case strings.HasPrefix(cmd, "/peer"):
		parts := strings.Fields(cmd)
		if len(parts) < 2 {
			c.emitSystem("usage: /peer <address> [address...]")
			return nil
		}
		if c.network == nil {
			c.emitSystem("cannot add peers: network unavailable")
			return nil
		}

		contacted := 0
		for _, raw := range parts[1:] {
			addr, err := c.network.Resolve(raw)
			if err != nil {
				c.emitSystem("failed to resolve %s: %v", raw, err)
				continue
			}
			c.markPending(addr)
			if err := c.sendDirect(addr, joinMsg, ""); err != nil {
				c.emitSystem("failed to reach %s: %v", raw, err)
				_ = c.dropPeer(addr, fmt.Sprintf("failed: %v", err))
				continue
			}
			c.markActive(addr)
			contacted++
		}

		if contacted > 0 {
			c.emitSystem("sent join to %d peer(s)", contacted)
		}
		return nil
	case strings.HasPrefix(cmd, "/switch"):
		parts := strings.Fields(cmd)
		if len(parts) != 2 {
			c.emitSystem("usage: /switch <config>")
			return nil
		}
		if c.config == nil {
			c.emitSystem("config switching is not available")
			return nil
		}
		if err := c.switchConfig(parts[1]); err != nil {
			return err
		}
		return nil
	default:
		c.emitSystem("unknown command %q", cmd)
		return nil
	}
}

func (c *Chat) switchConfig(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		c.emitSystem("usage: /switch <config>")
		return nil
	}

	cfg, err := ResolveProfile(c.config, trimmed)
	if err != nil {
		c.emitSystem("failed to load config %q: %v", trimmed, err)
		return nil
	}

	if cfg.Listen != "" && cfg.Listen != c.listenAddr {
		c.emitSystem("config %q uses listen %s; restart required to apply (current %s)", trimmed, cfg.Listen, c.listenAddr)
		return nil
	}

	var newCipher Cipher
	if cfg.Secret != "" {
		newCipher, err = NewAESCipher(cfg.Secret)
		if err != nil {
			c.emitSystem("config %q secret rejected: %v", trimmed, err)
			return nil
		}
	}

	resolved := make([]net.Addr, 0, len(cfg.Peers))
	for _, peer := range cfg.Peers {
		addr, err := c.network.Resolve(peer)
		if err != nil {
			c.emitSystem("config %q skipping %s: %v", trimmed, peer, err)
			continue
		}
		resolved = append(resolved, addr)
	}

	if len(c.peers.Snapshot()) > 0 {
		if err := c.broadcast(leaveMsg, ""); err != nil {
			c.emitSystem("failed to send leave notice: %v", err)
		}
	}

	prevSecret := c.secret
	c.secret = cfg.Secret
	c.cipher = newCipher

	if (prevSecret == "" && cfg.Secret != "") || (prevSecret != "" && cfg.Secret == "") {
		if cfg.Secret == "" {
			c.emitSystem("encryption disabled")
		} else {
			c.emitSystem("encryption enabled")
		}
	}

	if cfg.Name != "" && cfg.Name != c.name {
		c.name = cfg.Name
		c.emitPromptUpdate(cfg.Name)
		c.emitSystem("now chatting as %s", cfg.Name)
	}

	c.peers = newPeerManager()
	c.statusMu.Lock()
	c.pending = make(map[string]net.Addr)
	c.statusMu.Unlock()
	c.bootstrap = append([]net.Addr(nil), resolved...)

	contacted := 0
	for _, addr := range resolved {
		c.markPending(addr)
		if err := c.sendDirect(addr, joinMsg, ""); err != nil {
			c.emitSystem("failed to reach %s: %v", addr, err)
			_ = c.dropPeer(addr, fmt.Sprintf("failed: %v", err))
			continue
		}
		c.markActive(addr)
		contacted++
	}

	if contacted == 0 && len(resolved) > 0 {
		if err := c.broadcast(joinMsg, ""); err != nil {
			c.emitSystem("failed to announce presence: %v", err)
		}
	}

	if len(resolved) == 0 {
		c.emitSystem("switched to %q with no peers; waiting for connections", trimmed)
	} else {
		c.emitSystem("switched to %q with %d peer(s)", trimmed, len(resolved))
	}
	c.recordEvent("switched to %q", trimmed)

	return nil
}
