package chat

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

type Cipher interface {
	Encrypt(plain []byte) ([]byte, []byte, error)
	Decrypt(nonce, ciphertext []byte) ([]byte, error)
}

type AESCipher struct {
	gcm cipher.AEAD
}

func NewAESCipher(secret string) (Cipher, error) {
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

	return &AESCipher{gcm: gcm}, nil
}

func (c *AESCipher) Encrypt(plain []byte) ([]byte, []byte, error) {
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	ciphertext := c.gcm.Seal(nil, nonce, plain, nil)
	return nonce, ciphertext, nil
}

func (c *AESCipher) Decrypt(nonce, ciphertext []byte) ([]byte, error) {
	return c.gcm.Open(nil, nonce, ciphertext, nil)
}
