package signer

import (
	"context"
	"fmt"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
)

// LocalSigner signs transactions using a local Ed25519 private key.
type LocalSigner struct {
	key     *crypto.Ed25519PrivateKey
	address aptossdk.AccountAddress
}

// NewLocalSigner creates a signer from a hex-encoded Ed25519 private key.
func NewLocalSigner(hexKey string) (*LocalSigner, error) {
	key := &crypto.Ed25519PrivateKey{}
	if err := key.FromHex(hexKey); err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	// Derive the account address from the authentication key
	authKey := key.AuthKey()
	var addr aptossdk.AccountAddress
	copy(addr[:], authKey[:])

	return &LocalSigner{key: key, address: addr}, nil
}

func (s *LocalSigner) Sign(_ context.Context, message []byte) (*crypto.AccountAuthenticator, error) {
	return s.key.Sign(message)
}

func (s *LocalSigner) PublicKey() crypto.PublicKey {
	return s.key.PubKey()
}

func (s *LocalSigner) Address() aptossdk.AccountAddress {
	return s.address
}
