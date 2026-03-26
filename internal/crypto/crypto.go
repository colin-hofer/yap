package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

var crockfordEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

const inviteTokenVersion = "Y1"

// InviteToken is a parsed shareable invite reference.
type InviteToken struct {
	PeerID string
	Code   string
}

// RandomID returns a short random identifier suitable for transcripts and swarms.
func RandomID(length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("length must be positive")
	}
	size := (length * 5 / 8) + 2
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return crockfordEncoding.EncodeToString(buf)[:length], nil
}

// NewInviteCode creates a 10-character invite code.
func NewInviteCode() (string, error) {
	return RandomID(10)
}

// NormalizeInviteCode strips separators and uppercases the code.
func NormalizeInviteCode(raw string) string {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	raw = strings.ReplaceAll(raw, "-", "")
	raw = strings.ReplaceAll(raw, " ", "")
	return raw
}

// FormatInviteToken renders a shareable invite token that carries the inviter peer identity.
func FormatInviteToken(peerID, code string) string {
	code = NormalizeInviteCode(code)
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return code
	}
	return inviteTokenVersion + "-" + peerID + "-" + code
}

// ParseInviteToken parses either a legacy short invite code or a versioned token.
func ParseInviteToken(raw string) (InviteToken, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return InviteToken{}, fmt.Errorf("invite code cannot be empty")
	}

	if strings.HasPrefix(strings.ToUpper(raw), inviteTokenVersion+"-") {
		parts := strings.SplitN(raw, "-", 3)
		if len(parts) != 3 {
			return InviteToken{}, fmt.Errorf("invalid invite token")
		}
		peerID := strings.TrimSpace(parts[1])
		code := NormalizeInviteCode(parts[2])
		if peerID == "" || code == "" {
			return InviteToken{}, fmt.Errorf("invalid invite token")
		}
		return InviteToken{PeerID: peerID, Code: code}, nil
	}

	code := NormalizeInviteCode(raw)
	if code == "" {
		return InviteToken{}, fmt.Errorf("invite code cannot be empty")
	}
	return InviteToken{Code: code}, nil
}

// NewRoomKey returns a base64-encoded 32-byte room key.
func NewRoomKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read room key bytes: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// Fingerprint returns a short hex fingerprint for a public identifier.
func Fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	encoded := hex.EncodeToString(sum[:])
	if len(encoded) < 16 {
		return encoded
	}
	return strings.ToUpper(encoded[:16])
}

// Encrypt seals plaintext with AES-256-GCM using the provided base64 key.
func Encrypt(keyBase64 string, plaintext []byte) (nonceBase64, cipherBase64 string, err error) {
	block, err := newBlock(keyBase64)
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", fmt.Errorf("new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", fmt.Errorf("read nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(nonce), base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt opens AES-256-GCM data using the provided base64 key.
func Decrypt(keyBase64, nonceBase64, cipherBase64 string) ([]byte, error) {
	block, err := newBlock(keyBase64)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(nonceBase64)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(cipherBase64)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt payload: %w", err)
	}
	return plain, nil
}

func newBlock(keyBase64 string) (cipher.Block, error) {
	key, err := base64.StdEncoding.DecodeString(keyBase64)
	if err != nil {
		return nil, fmt.Errorf("decode room key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("room key must decode to 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	return block, nil
}
