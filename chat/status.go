package chat

import (
	"fmt"
	"net"
	"sort"
	"strings"
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

func (c *Chat) pendingSnapshot() []string {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()

	if len(c.pending) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.pending))
	for key := range c.pending {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func (c *Chat) lastEventValue() string {
	c.statusMu.RLock()
	defer c.statusMu.RUnlock()
	return c.lastEvent
}

func (c *Chat) markPending(addr net.Addr) {
	if addr == nil || c.peers.Has(addr) {
		return
	}
	key := addr.String()
	added := false
	c.statusMu.Lock()
	if c.pending == nil {
		c.pending = make(map[string]net.Addr)
	}
	if _, ok := c.pending[key]; !ok {
		c.pending[key] = addr
		added = true
	}
	c.statusMu.Unlock()
	if added {
		c.recordEvent("contacting %s", key)
	}
}

func (c *Chat) markActive(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	added := c.peers.Add(addr)
	key := addr.String()
	wasPending := false
	c.statusMu.Lock()
	if _, ok := c.pending[key]; ok {
		delete(c.pending, key)
		wasPending = true
	}
	c.statusMu.Unlock()
	if added || wasPending {
		c.recordEvent("connected %s", key)
	}
	return added
}

func (c *Chat) dropPeer(addr net.Addr, reason string) bool {
	if addr == nil {
		return false
	}
	key := addr.String()
	wasActive := c.peers.Drop(addr)
	wasPending := false
	c.statusMu.Lock()
	if _, ok := c.pending[key]; ok {
		delete(c.pending, key)
		wasPending = true
	}
	c.statusMu.Unlock()
	if !wasActive && !wasPending {
		return false
	}
	event := reason
	if event == "" {
		event = fmt.Sprintf("disconnected %s", key)
	} else if !strings.Contains(event, key) {
		event = fmt.Sprintf("%s: %s", key, event)
	}
	c.recordEvent(event)
	return true
}

func (c *Chat) recordEvent(format string, args ...any) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	c.lastEvent = fmt.Sprintf(format, args...)
}

func (c *Chat) describePeers() string {
	active := c.peers.Snapshot()
	sort.Strings(active)
	pending := c.pendingSnapshot()
	last := c.lastEventValue()

	encState := "disabled"
	if c.cipher != nil {
		encState = "enabled"
	}

	var b strings.Builder
	b.WriteString("┌ peers summary\n")
	b.WriteString(fmt.Sprintf("│ encryption: %s\n", encState))
	b.WriteString(fmt.Sprintf("│ active (%d): %s\n", len(active), summarizeList(active)))
	b.WriteString(fmt.Sprintf("│ pending (%d): %s\n", len(pending), summarizeList(pending)))
	if last != "" {
		b.WriteString(fmt.Sprintf("│ last event: %s\n", last))
	} else {
		b.WriteString("│ last event: n/a\n")
	}
	b.WriteString("└")
	return b.String()
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
