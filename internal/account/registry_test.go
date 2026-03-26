package account

import (
	"context"
	"testing"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
)

// stubSigner is a minimal signer for testing the registry.
type stubSigner struct {
	addr aptossdk.AccountAddress
}

func (s *stubSigner) Sign(_ context.Context, _ []byte) (*crypto.AccountAuthenticator, error) {
	return nil, nil
}
func (s *stubSigner) PublicKey() crypto.PublicKey      { return nil }
func (s *stubSigner) Address() aptossdk.AccountAddress { return s.addr }

func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := NewRegistry()
	s := &stubSigner{addr: aptossdk.AccountAddress{1}}
	reg.Register(s)

	addr := s.Address()
	got, err := reg.Get(addr.String())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Address() != s.addr {
		t.Errorf("got address %v, want %v", got.Address(), s.addr)
	}
}

func TestRegistry_GetMissingAddress(t *testing.T) {
	reg := NewRegistry()

	_, err := reg.Get("0x9999")
	if err == nil {
		t.Fatal("expected error for missing address")
	}
}

func TestRegistry_OverwriteAddress(t *testing.T) {
	reg := NewRegistry()
	// Same address, two different signers — second should win
	s1 := &stubSigner{addr: aptossdk.AccountAddress{1}}
	s2 := &stubSigner{addr: aptossdk.AccountAddress{1}}

	reg.Register(s1)
	reg.Register(s2)

	addr := s1.Address()
	got, err := reg.Get(addr.String())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != s2 {
		t.Errorf("expected latest registration to win")
	}
}
