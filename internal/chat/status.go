package chat

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// emit attempts to queue a message onto the session's event channel.
func (s *session) emit(msg Message) {
	defer func() {
		_ = recover()
	}()
	select {
	case <-s.closed:
		return
	default:
	}

	select {
	case s.events <- msg:
	default:
		select {
		case <-s.events:
		case <-s.closed:
			return
		}
		select {
		case s.events <- msg:
		case <-s.closed:
		}
	}
}

// emitSystem formats and emits a system notification message.
func (s *session) emitSystem(format string, args ...any) {
	s.emit(Message{Type: systemMsg, Body: fmt.Sprintf(format, args...)})
}

// emitPromptUpdate pushes a prompt update for UI refreshes.
func (s *session) emitPromptUpdate(name string) {
	s.emit(Message{Type: promptMsg, Body: name})
}

// lastEventValue safely returns the most recent status event string.
func (s *session) lastEventValue() string {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return s.lastEvent
}

// markPending updates membership when we attempt to contact a peer.
func (s *session) markPending(addr net.Addr) {
	if addr == nil {
		return
	}
	addrStr := canonicalNetAddr(addr)
	added := s.addPendingMember(addrStr)
	if added {
		s.recordEvent("contacting %s", addrStr)
	}
}

// markActive records a successful peer connection and caches its endpoint.
func (s *session) markActive(addr net.Addr, name string) bool {
	if addr == nil {
		return false
	}
	addrStr := canonicalNetAddr(addr)
	if addrStr == "" {
		return false
	}
	if ap, ok := addrPortFromNet(addr); ok {
		s.setMemberEndpoint(addrStr, ap)
	}
	transitioned := s.markMemberActive(addrStr, name)
	if transitioned {
		s.recordEvent("connected %s", addrStr)
	}
	return transitioned
}

// dropPeer reacts to peer departure or failure, updating state and events.
func (s *session) dropPeer(addr net.Addr, reason string) bool {
	if addr == nil {
		return false
	}
	addrStr := canonicalNetAddr(addr)
	var changed bool
	if reason == "left the chat" {
		changed = s.removeMember(addrStr)
	} else {
		changed = s.markMemberFailed(addrStr)
	}
	if !changed {
		return false
	}
	event := reason
	if event == "" {
		event = fmt.Sprintf("disconnected %s", addrStr)
	} else if !strings.Contains(event, addrStr) {
		event = fmt.Sprintf("%s: %s", addrStr, event)
	}
	s.recordEvent("%s", event)
	return true
}

// recordEvent stores a formatted string as the latest status update.
func (s *session) recordEvent(format string, args ...any) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.lastEvent = fmt.Sprintf(format, args...)
}

// peersSummary builds a human readable view of connection status.
func (s *session) peersSummary() string {
	var active []string
	var pending []string
	activeMembers, pendingMembers := s.membersSnapshot()
	active = formatMemberAddrs(activeMembers)
	pending = formatMemberAddrs(pendingMembers)
	lines := []string{
		fmt.Sprintf("active (%d): %s", len(active), summarizeList(active)),
		fmt.Sprintf("pending (%d): %s", len(pending), summarizeList(pending)),
	}
	if s.transport != nil {
		state := "disabled"
		if s.transport.encryptionEnabled() {
			state = "enabled"
		}
		lines = append(lines, fmt.Sprintf("encryption: %s", state))
	}
	if last := s.lastEventValue(); last != "" {
		lines = append(lines, fmt.Sprintf("last event: %s", last))
	}
	return strings.Join(lines, "\n")
}

// formatMemberAddrs renders members with optional names for display.
func formatMemberAddrs(members []member) []string {
	if len(members) == 0 {
		return nil
	}
	list := make([]string, 0, len(members))
	for _, member := range members {
		label := member.Addr
		if member.Name != "" {
			label = fmt.Sprintf("%s (%s)", member.Addr, member.Name)
		}
		list = append(list, label)
	}
	sort.Strings(list)
	return list
}

// summarizeList produces a compact summary for logging or UI.
func summarizeList(items []string) string {
	switch len(items) {
	case 0:
		return "none"
	case 1:
		return items[0]
	case 2:
		return strings.Join(items, ", ")
	default:
		return fmt.Sprintf("%s, %s (+%d more)", items[0], items[1], len(items)-2)
	}
}
