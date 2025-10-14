package chat

import (
	"fmt"
	"net"
	"strings"

	"yap/internal/config"
	"yap/internal/membership"
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
		c.emitSystem("%s", c.peersSummary())
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
		if c.store == nil {
			c.emitSystem("config saving is not available")
			return nil
		}
		groupName := parts[1]
		var active, pending []string
		if c.members != nil {
			active = c.members.ActiveAddrs()
			pending = c.members.PendingAddrs()
		} else {
			active = c.addressKeys()
		}
		snapshot := config.Snapshot(c.cfg.Name, c.cfg.Listen, c.cfg.Secret, active, pending)
		if err := c.store.Save(groupName, snapshot); err != nil {
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

		contacted := 0
		for _, raw := range parts[1:] {
			addr, err := c.resolveAddr(raw)
			if err != nil {
				c.emitSystem("failed to resolve %s: %v", raw, err)
				continue
			}
			c.markPending(addr)
			if err := c.sendDirect(addr, joinMsg, c.buildJoinPayload()); err != nil {
				c.emitSystem("failed to reach %s: %v", raw, err)
				_ = c.dropPeer(addr, fmt.Sprintf("failed: %v", err))
				continue
			}
			c.markActive(addr, "")
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
		if c.store == nil {
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

	cfg, err := config.ResolveProfile(c.store, trimmed)
	if err != nil {
		c.emitSystem("failed to load config %q: %v", trimmed, err)
		return nil
	}

	if cfg.Listen != "" && cfg.Listen != c.cfg.Listen {
		c.emitSystem("config %q uses listen %s; restart required to apply (current %s)", trimmed, cfg.Listen, c.cfg.Listen)
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
		addr, err := c.resolveAddr(peer)
		if err != nil {
			c.emitSystem("config %q skipping %s: %v", trimmed, peer, err)
			continue
		}
		resolved = append(resolved, addr)
	}

	known := 0
	if c.members != nil {
		known = len(c.members.ActiveAddrs())
	} else {
		c.addrMu.RLock()
		known = len(c.addresses)
		c.addrMu.RUnlock()
	}
	if known > 0 {
		if err := c.broadcast(leaveMsg, ""); err != nil {
			c.emitSystem("failed to send leave notice: %v", err)
		}
	}

	prevSecret := c.cfg.Secret
	c.cfg.Secret = cfg.Secret
	if c.transport != nil {
		c.transport.SetCipher(newCipher)
		c.transport.SetName(cfg.Name)
	}

	if (prevSecret == "" && cfg.Secret != "") || (prevSecret != "" && cfg.Secret == "") {
		if cfg.Secret == "" {
			c.emitSystem("encryption disabled")
		} else {
			c.emitSystem("encryption enabled")
		}
	}

	if cfg.Name != "" && cfg.Name != c.cfg.Name {
		c.cfg.Name = cfg.Name
		c.emitPromptUpdate(cfg.Name)
		c.emitSystem("now chatting as %s", cfg.Name)
	}

	if c.members != nil {
		local := ""
		if c.transport != nil {
			if addr := c.transport.LocalAddr(); addr != nil {
				local = addr.String()
			}
		}
		c.members = membership.New(local, c.cfg.Name)
	}
	c.addrMu.Lock()
	c.addresses = make(map[string]net.Addr)
	c.addrMu.Unlock()
	c.bootstrap = append([]net.Addr(nil), resolved...)

	joinPayload := c.buildJoinPayload()
	contacted := 0
	for _, addr := range resolved {
		c.markPending(addr)
		if err := c.sendDirect(addr, joinMsg, joinPayload); err != nil {
			c.emitSystem("failed to reach %s: %v", addr, err)
			_ = c.dropPeer(addr, fmt.Sprintf("failed: %v", err))
			continue
		}
		c.markActive(addr, "")
		contacted++
	}

	if contacted == 0 && len(resolved) > 0 {
		if err := c.broadcast(joinMsg, joinPayload); err != nil {
			c.emitSystem("failed to announce presence: %v", err)
		}
	}

	if len(resolved) == 0 {
		c.emitSystem("switched to %q with no peers; waiting for connections", trimmed)
	} else {
		c.emitSystem("switched to %q with %d peer(s)", trimmed, len(resolved))
	}
	if summary := config.Summary(cfg); len(summary) > 0 {
		c.emitSystem(strings.Join(summary, "\n"))
	}
	c.cfg = cfg
	c.recordEvent("switched to %q", trimmed)

	return nil
}
