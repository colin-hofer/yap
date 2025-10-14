package chat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

func (c *Chat) listen() {
	buf := make([]byte, 4096)
	for {
		if err := c.conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			select {
			case <-c.closed:
				return
			default:
				c.emitSystem("read deadline error: %v", err)
				return
			}
		}
		length, addr, err := c.conn.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				select {
				case <-c.closed:
					return
				default:
					continue
				}
			}
			select {
			case <-c.closed:
				return
			default:
				c.emitSystem("read error: %v", err)
				continue
			}
		}

		data := make([]byte, length)
		copy(data, buf[:length])
		go c.handlePacket(data, addr)
	}
}

func (c *Chat) handlePacket(data []byte, addr net.Addr) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		c.emitSystem("discarded malformed packet from %s", addr)
		return
	}

	if _, seen := c.seen.LoadOrStore(msg.ID, struct{}{}); seen {
		return
	}

	authenticated, err := c.verifyAndDecrypt(&msg, addr)
	if err != nil {
		// Silently ignore unauthorized messages on receiver side.
		return
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
			c.markActive(addr)
		}
	}

	c.emit(msg)
	c.forward(data, addr)
}

func (c *Chat) verifyAndDecrypt(msg *Message, addr net.Addr) (bool, error) {
	if msg.Type == errorMsg {
		return false, nil
	}

	encrypted := msg.Cipher != ""

	if c.cipher == nil {
		if encrypted {
			c.sendAuthReject(addr, "encryption required")
			return false, fmt.Errorf("ignored encrypted message from %s (secret required)", msg.From)
		}
		return true, nil
	}

	if !encrypted {
		c.sendAuthReject(addr, "encryption required")
		return false, fmt.Errorf("rejected unencrypted message from %s", msg.From)
	}

	nonce, err := base64.StdEncoding.DecodeString(msg.Nonce)
	if err != nil {
		c.sendAuthReject(addr, "invalid nonce")
		return false, fmt.Errorf("bad nonce from %s", msg.From)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(msg.Cipher)
	if err != nil {
		c.sendAuthReject(addr, "invalid ciphertext")
		return false, fmt.Errorf("bad ciphertext from %s", msg.From)
	}
	plain, err := c.cipher.Decrypt(nonce, ciphertext)
	if err != nil {
		c.sendAuthReject(addr, "authentication failed")
		return false, fmt.Errorf("failed to decrypt message from %s", msg.From)
	}
	msg.Body = string(plain)
	return true, nil
}

func (c *Chat) sendAuthReject(addr net.Addr, reason string) {
	msg := Message{
		ID:        newMessageID(),
		From:      c.name,
		Type:      errorMsg,
		Body:      reason,
		Timestamp: time.Now().Unix(),
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_, _ = c.conn.WriteTo(raw, addr)
	c.emit(msg)
	_ = c.dropPeer(addr, reason)
}

func (c *Chat) broadcast(kind msgType, body string) error {
	msg, raw, err := c.prepareMessage(kind, body)
	if err != nil {
		return err
	}

	if kind == chatMsg {
		localMsg := msg
		localMsg.Body = body
		localMsg.Cipher = ""
		localMsg.Nonce = ""
		c.emit(localMsg)
	}

	c.forward(raw, nil)
	return nil
}

func (c *Chat) forward(data []byte, exclude net.Addr) {
	peers := c.peers.List(exclude)
	for _, addr := range peers {
		if _, err := c.conn.WriteTo(data, addr); err != nil {
			c.emitSystem("send to %s failed: %v", addr, err)
		}
	}
}

func (c *Chat) prepareMessage(kind msgType, body string) (Message, []byte, error) {
	msg := Message{
		ID:        newMessageID(),
		From:      c.name,
		Body:      body,
		Type:      kind,
		Timestamp: time.Now().Unix(),
	}

	if c.cipher != nil {
		nonce, ciphertext, err := c.cipher.Encrypt([]byte(body))
		if err != nil {
			return Message{}, nil, fmt.Errorf("encrypt message: %w", err)
		}
		msg.Cipher = base64.StdEncoding.EncodeToString(ciphertext)
		msg.Nonce = base64.StdEncoding.EncodeToString(nonce)
		msg.Body = ""
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		return Message{}, nil, fmt.Errorf("encode message: %w", err)
	}

	c.seen.Store(msg.ID, struct{}{})
	return msg, raw, nil
}

func (c *Chat) sendDirect(addr net.Addr, kind msgType, body string) error {
	_, raw, err := c.prepareMessage(kind, body)
	if err != nil {
		return err
	}
	_, err = c.conn.WriteTo(raw, addr)
	return err
}
