package chat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// transport handles encoding and network IO for the session.
type transport struct {
	name   string
	conn   net.PacketConn
	seen   sync.Map
	mu     sync.RWMutex
	cipher packetCipher
}

// newTransport wires up the UDP socket and optional cipher wrapper.
func newTransport(name string, conn net.PacketConn, cipher packetCipher) *transport {
	return &transport{name: name, conn: conn, cipher: cipher}
}

// localAddr exposes the underlying socket's bound address.
func (t *transport) localAddr() net.Addr {
	return t.conn.LocalAddr()
}

// encryptionEnabled reports whether a cipher has been configured.
func (t *transport) encryptionEnabled() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cipher != nil
}

// setCipher swaps the active cipher to use for subsequent messages.
func (t *transport) setCipher(cipher packetCipher) {
	t.mu.Lock()
	t.cipher = cipher
	t.mu.Unlock()
}

// setName updates the sender name used in outbound messages.
func (t *transport) setName(name string) {
	t.mu.Lock()
	t.name = name
	t.mu.Unlock()
}

// close releases the underlying socket resources.
func (t *transport) close() error {
	return t.conn.Close()
}

// listen consumes packets from the socket and hands them to the session callbacks.
func (t *transport) listen(stop <-chan struct{}, handle func(Message, net.Addr, []byte, bool), reject func(Message, net.Addr), system func(string, ...any)) {
	go func() {
		buf := make([]byte, 4096)
		for {
			if err := t.conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				select {
				case <-stop:
					return
				default:
					if system != nil {
						system("read deadline error: %v", err)
					}
					return
				}
			}
			length, addr, err := t.conn.ReadFrom(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					select {
					case <-stop:
						return
					default:
						continue
					}
				}
				select {
				case <-stop:
					return
				default:
					if system != nil {
						system("read error: %v", err)
					}
					continue
				}
			}

			data := make([]byte, length)
			copy(data, buf[:length])

			var msg Message
			if err := json.Unmarshal(data, &msg); err != nil {
				if system != nil {
					system("discarded malformed packet from %s", addr)
				}
				continue
			}

			if _, seen := t.seen.LoadOrStore(msg.ID, struct{}{}); seen {
				continue
			}

			authenticated, reason, err := t.verifyAndDecrypt(&msg)
			if err != nil {
				if reason != "" {
					rejectMsg, sendErr := t.reject(addr, reason)
					if system != nil && sendErr != nil {
						system("failed to send reject to %s: %v", addr, sendErr)
					}
					if reject != nil && rejectMsg.ID != "" {
						reject(rejectMsg, addr)
					}
				} else if system != nil {
					system("%v", err)
				}
				continue
			}

			if handle != nil {
				go func(m Message, a net.Addr, d []byte, auth bool) {
					handle(m, a, d, auth)
				}(msg, addr, data, authenticated)
			}
		}
	}()
}

// prepare assembles, encrypts, and marshals an outbound message.
func (t *transport) prepare(name string, kind msgType, body string) (Message, []byte, error) {
	msg := Message{
		ID:        newMessageID(),
		From:      name,
		Body:      body,
		Type:      kind,
		Timestamp: time.Now().Unix(),
	}

	if cipher := t.currentCipher(); cipher != nil {
		nonce, ciphertext, err := cipher.Encrypt([]byte(body))
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

	t.seen.Store(msg.ID, struct{}{})
	return msg, raw, nil
}

// sendRaw writes an encoded packet to the specified network address.
func (t *transport) sendRaw(addr net.Addr, data []byte) error {
	_, err := t.conn.WriteTo(data, addr)
	return err
}

// verifyAndDecrypt authenticates inbound payloads and restores plaintext bodies.
func (t *transport) verifyAndDecrypt(msg *Message) (bool, string, error) {
	if msg.Type == errorMsg {
		return false, "", nil
	}

	encrypted := msg.Cipher != ""

	cipher := t.currentCipher()
	if cipher == nil {
		if encrypted {
			return false, "encryption required", fmt.Errorf("ignored encrypted message from %s (secret required)", msg.From)
		}
		return true, "", nil
	}

	if !encrypted {
		return false, "encryption required", fmt.Errorf("rejected unencrypted message from %s", msg.From)
	}

	nonce, err := base64.StdEncoding.DecodeString(msg.Nonce)
	if err != nil {
		return false, "invalid nonce", fmt.Errorf("bad nonce from %s", msg.From)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(msg.Cipher)
	if err != nil {
		return false, "invalid ciphertext", fmt.Errorf("bad ciphertext from %s", msg.From)
	}
	plain, err := cipher.Decrypt(nonce, ciphertext)
	if err != nil {
		return false, "authentication failed", fmt.Errorf("failed to decrypt message from %s", msg.From)
	}
	msg.Body = string(plain)
	return true, "", nil
}

// reject sends an error response back to a peer that failed authentication.
func (t *transport) reject(addr net.Addr, reason string) (Message, error) {
	msg := Message{
		ID:        newMessageID(),
		From:      t.name,
		Type:      errorMsg,
		Body:      reason,
		Timestamp: time.Now().Unix(),
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		return Message{}, err
	}
	if _, err := t.conn.WriteTo(raw, addr); err != nil {
		return msg, err
	}
	return msg, nil
}

// currentCipher safely retrieves the currently configured cipher instance.
func (t *transport) currentCipher() packetCipher {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cipher
}
