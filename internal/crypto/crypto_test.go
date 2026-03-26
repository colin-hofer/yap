package crypto

import "testing"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key, err := NewRoomKey()
	if err != nil {
		t.Fatalf("NewRoomKey() error = %v", err)
	}

	nonce, ciphertext, err := Encrypt(key, []byte("hello swarm"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	plaintext, err := Decrypt(key, nonce, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}

	if got, want := string(plaintext), "hello swarm"; got != want {
		t.Fatalf("plaintext = %q, want %q", got, want)
	}
}

func TestDecryptRejectsTampering(t *testing.T) {
	key, err := NewRoomKey()
	if err != nil {
		t.Fatalf("NewRoomKey() error = %v", err)
	}

	nonce, ciphertext, err := Encrypt(key, []byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	tampered := ciphertext[:len(ciphertext)-1] + "A"
	if _, err := Decrypt(key, nonce, tampered); err == nil {
		t.Fatal("Decrypt() unexpectedly accepted tampered ciphertext")
	}
}

func TestNormalizeInviteCode(t *testing.T) {
	got := NormalizeInviteCode(" abcd-1234 ")
	if want := "ABCD1234"; got != want {
		t.Fatalf("NormalizeInviteCode() = %q, want %q", got, want)
	}
}

func TestFormatInviteToken(t *testing.T) {
	got := FormatInviteToken("12D3KooWTestPeer", "abcd-1234")
	if want := "Y1-12D3KooWTestPeer-ABCD1234"; got != want {
		t.Fatalf("FormatInviteToken() = %q, want %q", got, want)
	}
}

func TestParseInviteTokenVersioned(t *testing.T) {
	token, err := ParseInviteToken("y1-12D3KooWTestPeer-abcd-1234")
	if err != nil {
		t.Fatalf("ParseInviteToken() error = %v", err)
	}
	if got, want := token.PeerID, "12D3KooWTestPeer"; got != want {
		t.Fatalf("PeerID = %q, want %q", got, want)
	}
	if got, want := token.Code, "ABCD1234"; got != want {
		t.Fatalf("Code = %q, want %q", got, want)
	}
}

func TestParseInviteTokenLegacy(t *testing.T) {
	token, err := ParseInviteToken(" abcd-1234 ")
	if err != nil {
		t.Fatalf("ParseInviteToken() error = %v", err)
	}
	if token.PeerID != "" {
		t.Fatalf("PeerID = %q, want empty", token.PeerID)
	}
	if got, want := token.Code, "ABCD1234"; got != want {
		t.Fatalf("Code = %q, want %q", got, want)
	}
}

func TestParseInviteTokenRejectsInvalidVersionedFormat(t *testing.T) {
	if _, err := ParseInviteToken("Y1--"); err == nil {
		t.Fatal("ParseInviteToken() unexpectedly accepted invalid token")
	}
}
