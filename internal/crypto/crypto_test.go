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
