package chat

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"yap/internal/membership"
)

func (c *Chat) emit(msg Message) {
	defer func() {
		_ = recover()
	}()
	select {
	case <-c.closed:
		return
	default:
	}

	select {
	case c.events <- msg:
	default:
		select {
		case <-c.events:
		case <-c.closed:
			return
		}
		select {
		case c.events <- msg:
		case <-c.closed:
		}
	}
}

func (c *Chat) emitSystem(format string, args ...any) {
	c.emit(Message{Type: systemMsg, Body: fmt.Sprintf(format, args...)})
}

func (c *Chat) emitPromptUpdate(name string) {
	c.emit(Message{Type: promptMsg, Body: name})
}

func (c *Chat) lastEventValue() string {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.lastEvent
}

func (c *Chat) markPending(addr net.Addr) {
	if addr == nil {
		return
	}
	addrStr := canonicalNetAddr(addr)
	var added bool
	if c.members != nil {
		added = c.members.AddPending(addrStr)
	}
	if added {
		c.recordEvent("contacting %s", addrStr)
	}
}

func (c *Chat) markActive(addr net.Addr, name string) bool {
	if addr == nil {
		return false
	}
	addrStr, added := c.rememberAddr(addr)
	if addrStr == "" {
		return false
	}
	var transitioned bool
	if c.members != nil {
		transitioned = c.members.MarkActive(addrStr, name)
	}
	if added || transitioned {
		c.recordEvent("connected %s", addrStr)
	}
	return added || transitioned
}

func (c *Chat) dropPeer(addr net.Addr, reason string) bool {
	if addr == nil {
		return false
	}
	addrStr := canonicalNetAddr(addr)
	removed := c.forgetAddr(addr)
	var changed bool
	if c.members != nil {
		if reason == "left the chat" {
			changed = c.members.Remove(addrStr)
		} else {
			changed = c.members.MarkFailed(addrStr)
		}
	}
	if !removed && !changed {
		return false
	}
	event := reason
	if event == "" {
		event = fmt.Sprintf("disconnected %s", addrStr)
	} else if !strings.Contains(event, addrStr) {
		event = fmt.Sprintf("%s: %s", addrStr, event)
	}
	c.recordEvent(event)
	return true
}

func (c *Chat) recordEvent(format string, args ...any) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	c.lastEvent = fmt.Sprintf(format, args...)
}

func (c *Chat) peersSummary() string {
	var active []string
	var pending []string
	if c.members != nil {
		activeMembers, pendingMembers := c.members.Snapshot()
		active = formatMemberAddrs(activeMembers)
		pending = formatMemberAddrs(pendingMembers)
	} else {
		active = c.addressKeys()
	}
	lines := []string{
		fmt.Sprintf("active (%d): %s", len(active), summarizeList(active)),
		fmt.Sprintf("pending (%d): %s", len(pending), summarizeList(pending)),
	}
	if c.transport != nil {
		state := "disabled"
		if c.transport.EncryptionEnabled() {
			state = "enabled"
		}
		lines = append(lines, fmt.Sprintf("encryption: %s", state))
	}
	if last := c.lastEventValue(); last != "" {
		lines = append(lines, fmt.Sprintf("last event: %s", last))
	}
	return strings.Join(lines, "\n")
}

func formatMemberAddrs(members []membership.Member) []string {
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
