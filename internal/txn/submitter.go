package txn

import (
	"context"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

// Submitter abstracts transaction submission and lookup for handlers.
type Submitter interface {
	// Submit sends a transaction to the blockchain, with the given operation
	Submit(ctx context.Context, op Operation) (string, error)
	// GetTransaction retrieves the transaction from the blockchain given an id
	GetTransaction(ctx context.Context, id string) (*store.TransactionRecord, error)
}
