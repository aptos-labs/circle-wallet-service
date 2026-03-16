package signer

import (
	"context"
	"testing"

	"github.com/aptos-labs/aptos-go-sdk/crypto"
)

func TestLocalSignerSignAndVerify(t *testing.T) {
	// Generate a random key for testing
	key, err := crypto.GenerateEd25519PrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	s, err := NewLocalSigner(key.ToHex())
	if err != nil {
		t.Fatalf("new local signer: %v", err)
	}

	// Sign a message
	msg := []byte("test message for signing")
	auth, err := s.Sign(context.Background(), msg)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if auth == nil {
		t.Fatal("expected authenticator, got nil")
	}
	if !auth.Verify(msg) {
		t.Fatal("expected authenticator signature to verify for original message")
	}
	if auth.Verify([]byte("tampered message")) {
		t.Fatal("expected authenticator signature verification to fail for tampered message")
	}

	// Verify the public key matches
	if s.PublicKey() == nil {
		t.Fatal("expected public key, got nil")
	}

	// Verify the address is non-zero
	addr := s.Address()
	allZero := true
	for _, b := range addr {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("expected non-zero address")
	}
}

func TestLocalSignerInvalidKey(t *testing.T) {
	_, err := NewLocalSigner("not-a-valid-hex-key")
	if err == nil {
		t.Error("expected error for invalid key")
	}
}
