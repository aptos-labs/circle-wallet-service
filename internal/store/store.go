// Package store defines the transaction persistence interfaces and data types
// shared across the API handlers, submitter, poller, and webhook subsystems.
//
// Two interfaces are defined:
//   - [Store] for basic CRUD and status queries (used by handlers and poller).
//   - [Queue] which extends Store with atomic claim, sequence management, and
//     recovery operations (used by the submitter worker).
//
// The MySQL implementation lives in [store/mysql]. An in-memory implementation
// exists for unit tests.
package store

import (
	"context"
	"errors"
	"time"
)

// TxnStatus represents the lifecycle state of a transaction.
//
//	queued → processing → submitted → confirmed
//	                                → failed
//	                                → expired
type TxnStatus string

const (
	StatusQueued     TxnStatus = "queued"
	StatusProcessing TxnStatus = "processing"
	StatusSubmitted  TxnStatus = "submitted"
	StatusConfirmed  TxnStatus = "confirmed"
	StatusFailed     TxnStatus = "failed"
	StatusExpired    TxnStatus = "expired"
)

// ErrIdempotencyConflict is returned when inserting a transaction with an idempotency key that already exists.
var ErrIdempotencyConflict = errors.New("idempotency key already exists")

// TransactionRecord is persisted for API responses and the submitter worker.
type TransactionRecord struct {
	ID               string    `json:"id"`
	IdempotencyKey   string    `json:"idempotency_key,omitempty"`
	Status           TxnStatus `json:"status"`
	TxnHash          string    `json:"txn_hash,omitempty"`
	SenderAddress    string    `json:"sender_address"`
	FunctionID       string    `json:"function_id"`
	WalletID         string    `json:"wallet_id"`
	FeePayerWalletID string    `json:"fee_payer_wallet_id,omitempty"`
	FeePayerAddress  string    `json:"fee_payer_address,omitempty"`
	PayloadJSON      string    `json:"payload_json,omitempty"`
	// SequenceNumber is set when the transaction is built for submit (Aptos account sequence).
	SequenceNumber *uint64   `json:"sequence_number,omitempty"`
	MaxGasAmount   *uint64   `json:"max_gas_amount,omitempty"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	WebhookURL     string    `json:"webhook_url,omitempty"`
	AttemptCount   int       `json:"attempt_count,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// QueuedPayload is stored in TransactionRecord.PayloadJSON for the worker to rebuild the entry function.
// Fee-payer fields live on TransactionRecord directly and are not duplicated here.
type QueuedPayload struct {
	TypeArguments []string `json:"type_arguments"`
	Arguments     []any    `json:"arguments"`
}

// Store provides CRUD and query operations for transaction records.
// Used by API handlers (create, get, list) and the poller (update status).
type Store interface {
	Create(ctx context.Context, rec *TransactionRecord) error
	Update(ctx context.Context, rec *TransactionRecord) error
	// UpdateIfStatus atomically updates the record only when its current status matches
	// expectedStatus. Returns false if another host already changed the status (no-op).
	UpdateIfStatus(ctx context.Context, rec *TransactionRecord, expectedStatus TxnStatus) (bool, error)
	Get(ctx context.Context, id string) (*TransactionRecord, error)
	GetByIdempotencyKey(ctx context.Context, key string) (*TransactionRecord, error)
	ListByStatus(ctx context.Context, status TxnStatus) ([]*TransactionRecord, error)
	Close() error
}

// Queue extends Store with the atomic claim/sequence operations needed by the
// submitter worker. Each method below is designed for multi-host safety using
// row-level locking (SELECT ... FOR UPDATE) in the MySQL implementation.
type Queue interface {
	Store
	// ClaimNextQueuedForSender atomically picks the oldest queued transaction for
	// the given sender, allocates the next Aptos sequence number, and transitions
	// it to "processing" — all inside a single SQL transaction.
	ClaimNextQueuedForSender(ctx context.Context, senderAddress string) (*TransactionRecord, error)
	// ListQueuedSenders returns distinct sender addresses that have queued work.
	ListQueuedSenders(ctx context.Context) ([]string, error)
	// ReconcileSequence advances the local sequence counter to at least chainSeq
	// (GREATEST). Used after an on-chain lookup to correct drift.
	ReconcileSequence(ctx context.Context, senderAddress string, chainSeq uint64) error
	// RecoverStaleProcessing resets transactions stuck in "processing" longer than
	// olderThan back to "queued" and decrements the sequence counter accordingly.
	RecoverStaleProcessing(ctx context.Context, olderThan time.Duration) (int64, error)
	// ShiftSenderSequences re-queues all transactions for a sender with sequence
	// numbers above failedSeqNum and adjusts the counter (atomic SQL transaction).
	ShiftSenderSequences(ctx context.Context, senderAddress string, failedSeqNum uint64) error
	// ReleaseSequence decrements the sender's sequence counter by 1, used when a
	// claimed transaction is returned to "queued" before submission.
	ReleaseSequence(ctx context.Context, senderAddress string) error
}
