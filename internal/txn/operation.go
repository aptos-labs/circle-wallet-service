package txn

import (
	"fmt"
	"sync"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

// Operation represents a blockchain operation that can be submitted as a transaction.
type Operation interface {
	// Name returns the operation type identifier (e.g., "mint", "burn").
	Name() string
	// RequiredRole returns the account role needed to sign this operation.
	RequiredRole() string
	// BuildPayload constructs the Aptos transaction payload.
	BuildPayload() (aptossdk.TransactionPayload, error)
	// RequestJSON returns the original request for audit logging.
	RequestJSON() []byte
}

// RecipientCounter is an optional interface for operations that target multiple recipients.
// Used by Manager.computeGas to scale gas proportionally.
type RecipientCounter interface {
	RecipientCount() int
}

// OperationFactory reconstructs an Operation from its stored JSON request payload.
type OperationFactory func(requestJSON []byte) (Operation, error)

var (
	factoryMu sync.RWMutex
	factories = map[string]OperationFactory{}
)

// RegisterOperationFactory registers a factory for reconstructing operations of the given type.
func RegisterOperationFactory(opType string, f OperationFactory) {
	factoryMu.Lock()
	defer factoryMu.Unlock()
	factories[opType] = f
}

// RebuildOperation reconstructs an Operation from a stored TransactionRecord.
func RebuildOperation(rec *store.TransactionRecord) (Operation, error) {
	factoryMu.RLock()
	f, ok := factories[rec.OperationType]
	factoryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no factory registered for operation type %q", rec.OperationType)
	}
	return f([]byte(rec.RequestPayload))
}
