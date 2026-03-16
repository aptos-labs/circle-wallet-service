package signer

import (
	"context"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
)

// Signer abstracts transaction signing, allowing local keys or remote signing services.
type Signer interface {
	// Sign produces an authenticator for the given signing message bytes.
	Sign(ctx context.Context, message []byte) (*crypto.AccountAuthenticator, error)

	// PublicKey returns the signer's Ed25519 public key.
	PublicKey() crypto.PublicKey

	// Address returns the Aptos account address derived from this signer.
	Address() aptossdk.AccountAddress
}

// TransactionSigner extends Signer for backends that need the full raw transaction
// context (e.g., Circle's sign/transaction API requires BCS-serialized RawTransactionWithData).
type TransactionSigner interface {
	Signer
	SignTransaction(ctx context.Context, rawTxn *aptossdk.RawTransaction) (*aptossdk.SignedTransaction, error)
}
