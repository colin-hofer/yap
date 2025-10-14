package chat

import (
	"fmt"
	"net"
	"strings"

	"yap/internal/config"
)

// handleInput routes user input to either command handling or broadcast.
func (s *session) handleInput(text string) error {
	switch {
	case text == "":
		return nil
	case strings.HasPrefix(text, "/"):
		return s.handleCommand(text)
	default:
		return s.broadcast(chatMsg, text)
	}
}

// handleCommand interprets slash commands and executes the requested action.
func (s *session) handleCommand(cmd string) error {
	switch {
	case cmd == "/peers":
		s.emitSystem("%s", s.peersSummary())
		return nil
	case cmd == "/quit" || cmd == "/exit" || cmd == "/q":
		s.emitSystem("goodbye")
		return errQuit
	case strings.HasPrefix(cmd, "/group"):
		parts := strings.Fields(cmd)
		if len(parts) != 2 {
			s.emitSystem("usage: /group <name>")
			return nil
		}
		if s.store == nil {
			s.emitSystem("config saving is not available")
			return nil
		}
		groupName := parts[1]
		active := s.activeAddrs()
		pending := s.pendingAddrs()
		snapshot := config.Snapshot(s.cfg.Name, s.cfg.Listen, s.cfg.Secret, active, pending)
		if err := s.store.Save(groupName, snapshot); err != nil {
			s.emitSystem("failed to save config: %v", err)
		} else {
			s.emitSystem("saved config %q with %d peers", groupName, len(snapshot.Peers))
		}
		return nil
	case strings.HasPrefix(cmd, "/peer"):
		parts := strings.Fields(cmd)
		if len(parts) < 2 {
			s.emitSystem("usage: /peer <address> [address...]")
			return nil
		}

		contacted := 0
		for _, raw := range parts[1:] {
			addr, err := s.resolveAddr(raw)
			if err != nil {
				s.emitSystem("failed to resolve %s: %v", raw, err)
				continue
			}
			s.markPending(addr)
			if err := s.sendDirect(addr, joinMsg, s.buildJoinPayload()); err != nil {
				s.emitSystem("failed to reach %s: %v", raw, err)
				_ = s.dropPeer(addr, fmt.Sprintf("failed: %v", err))
				continue
			}
			s.markActive(addr, "")
			contacted++
		}

		if contacted > 0 {
			s.emitSystem("sent join to %d peer(s)", contacted)
		}
		return nil
	case strings.HasPrefix(cmd, "/switch"):
		parts := strings.Fields(cmd)
		if len(parts) != 2 {
			s.emitSystem("usage: /switch <config>")
			return nil
		}
		if s.store == nil {
			s.emitSystem("config switching is not available")
			return nil
		}
		if err := s.switchConfig(parts[1]); err != nil {
			return err
		}
		return nil
	default:
		s.emitSystem("unknown command %q", cmd)
		return nil
	}
}

// switchConfig loads a saved profile and applies it to the running session.
func (s *session) switchConfig(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		s.emitSystem("usage: /switch <config>")
		return nil
	}

	cfg, err := config.ResolveProfile(s.store, trimmed)
	if err != nil {
		s.emitSystem("failed to load config %q: %v", trimmed, err)
		return nil
	}

	if cfg.Listen != "" && cfg.Listen != s.cfg.Listen {
		s.emitSystem("config %q uses listen %s; restart required to apply (current %s)", trimmed, cfg.Listen, s.cfg.Listen)
		return nil
	}

	var newCipher packetCipher
	if cfg.Secret != "" {
		newCipher, err = newAESCipher(cfg.Secret)
		if err != nil {
			s.emitSystem("config %q secret rejected: %v", trimmed, err)
			return nil
		}
	}

	resolved := make([]net.Addr, 0, len(cfg.Peers))
	for _, peer := range cfg.Peers {
		addr, err := s.resolveAddr(peer)
		if err != nil {
			s.emitSystem("config %q skipping %s: %v", trimmed, peer, err)
			continue
		}
		resolved = append(resolved, addr)
	}

	known := len(s.activeAddrs())
	if known > 0 {
		if err := s.broadcast(leaveMsg, ""); err != nil {
			s.emitSystem("failed to send leave notice: %v", err)
		}
	}

	prevSecret := s.cfg.Secret
	s.cfg.Secret = cfg.Secret
	if s.transport != nil {
		s.transport.setCipher(newCipher)
		s.transport.setName(cfg.Name)
	}

	if (prevSecret == "" && cfg.Secret != "") || (prevSecret != "" && cfg.Secret == "") {
		if cfg.Secret == "" {
			s.emitSystem("encryption disabled")
		} else {
			s.emitSystem("encryption enabled")
		}
	}

	if cfg.Name != "" && cfg.Name != s.cfg.Name {
		s.cfg.Name = cfg.Name
		s.emitPromptUpdate(cfg.Name)
		s.emitSystem("now chatting as %s", cfg.Name)
	}

	local := ""
	if s.transport != nil {
		if addr := s.transport.localAddr(); addr != nil {
			local = addr.String()
		}
	}
	s.resetMembership(local)
	s.bootstrap = append([]net.Addr(nil), resolved...)

	joinPayload := s.buildJoinPayload()
	contacted := 0
	for _, addr := range resolved {
		s.markPending(addr)
		if err := s.sendDirect(addr, joinMsg, joinPayload); err != nil {
			s.emitSystem("failed to reach %s: %v", addr, err)
			_ = s.dropPeer(addr, fmt.Sprintf("failed: %v", err))
			continue
		}
		s.markActive(addr, "")
		contacted++
	}

	if contacted == 0 && len(resolved) > 0 {
		if err := s.broadcast(joinMsg, joinPayload); err != nil {
			s.emitSystem("failed to announce presence: %v", err)
		}
	}

	if len(resolved) == 0 {
		s.emitSystem("switched to %q with no peers; waiting for connections", trimmed)
	} else {
		s.emitSystem("switched to %q with %d peer(s)", trimmed, len(resolved))
	}
	if summary := config.Summary(cfg); len(summary) > 0 {
		s.emitSystem("%s", strings.Join(summary, "\n"))
	}
	s.cfg = cfg
	s.recordEvent("switched to %q", trimmed)

	return nil
}
