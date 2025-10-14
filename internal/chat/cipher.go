package chat

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

// packetCipher defines the encryption contract used by the transport layer.
type packetCipher interface {
	Encrypt(plain []byte) ([]byte, []byte, error)
	Decrypt(nonce, ciphertext []byte) ([]byte, error)
}

type aesCipher struct {
	gcm cipher.AEAD
}

// newAESCipher constructs an AES-GCM cipher from the supplied secret.
func newAESCipher(secret string) (packetCipher, error) {
	if secret == "" {
		return nil, errors.New("secret cannot be empty")
	}

	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &aesCipher{gcm: gcm}, nil
}

// Encrypt applies AES-GCM and returns the nonce alongside the ciphertext.
func (c *aesCipher) Encrypt(plain []byte) ([]byte, []byte, error) {
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	ciphertext := c.gcm.Seal(nil, nonce, plain, nil)
	return nonce, ciphertext, nil
}

// Decrypt verifies and recovers the plaintext for a sealed message.
func (c *aesCipher) Decrypt(nonce, ciphertext []byte) ([]byte, error) {
	return c.gcm.Open(nil, nonce, ciphertext, nil)
}
