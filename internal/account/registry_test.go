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
	reg.Register("minter", s)

	got, err := reg.Get("minter")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Address() != s.addr {
		t.Errorf("got address %v, want %v", got.Address(), s.addr)
	}
}

func TestRegistry_GetMissingRole(t *testing.T) {
	reg := NewRegistry()

	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing role")
	}
}

func TestRegistry_OverwriteRole(t *testing.T) {
	reg := NewRegistry()
	s1 := &stubSigner{addr: aptossdk.AccountAddress{1}}
	s2 := &stubSigner{addr: aptossdk.AccountAddress{2}}

	reg.Register("minter", s1)
	reg.Register("minter", s2)

	got, err := reg.Get("minter")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Address() != s2.addr {
		t.Errorf("got address %v, want %v (latest registration)", got.Address(), s2.addr)
	}
}
