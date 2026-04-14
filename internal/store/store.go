package store

import (
	"context"
	"errors"
	"time"
)

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
type QueuedPayload struct {
	TypeArguments    []string `json:"type_arguments"`
	Arguments        []any    `json:"arguments"`
	FeePayerWalletID string   `json:"fee_payer_wallet_id,omitempty"`
	FeePayerAddress  string   `json:"fee_payer_address,omitempty"`
}

type Store interface {
	Create(ctx context.Context, rec *TransactionRecord) error
	Update(ctx context.Context, rec *TransactionRecord) error
	UpdateIfStatus(ctx context.Context, rec *TransactionRecord, expectedStatus TxnStatus) (bool, error)
	Get(ctx context.Context, id string) (*TransactionRecord, error)
	GetByIdempotencyKey(ctx context.Context, key string) (*TransactionRecord, error)
	ListByStatus(ctx context.Context, status TxnStatus) ([]*TransactionRecord, error)
	Close() error
}

// Queue extends Store with claim and sequence helpers for the submitter worker (MySQL).
type Queue interface {
	Store
	ClaimNextQueued(ctx context.Context) (*TransactionRecord, error)
	ClaimNextQueuedForSender(ctx context.Context, senderAddress string) (*TransactionRecord, error)
	ListQueuedSenders(ctx context.Context) ([]string, error)
	UpsertNextSequence(ctx context.Context, senderAddress string, next uint64) error
	ReconcileSequence(ctx context.Context, senderAddress string, chainSeq uint64) error
	RecoverStaleProcessing(ctx context.Context, olderThan time.Duration) (int64, error)
	ShiftSenderSequences(ctx context.Context, senderAddress string, failedSeqNum uint64) error
}
