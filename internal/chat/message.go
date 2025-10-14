package chat

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type msgType string

const (
	chatMsg   msgType = "chat"
	joinMsg   msgType = "join"
	leaveMsg  msgType = "leave"
	errorMsg  msgType = "error"
	systemMsg msgType = "system"
	promptMsg msgType = "prompt"
	peersMsg  msgType = "peers"
)

type Message struct {
	ID        string  `json:"id"`
	From      string  `json:"from"`
	Body      string  `json:"body"`
	Type      msgType `json:"kind"`
	Timestamp int64   `json:"timestamp"`
	Cipher    string  `json:"cipher,omitempty"`
	Nonce     string  `json:"nonce,omitempty"`
}

func newMessageID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
