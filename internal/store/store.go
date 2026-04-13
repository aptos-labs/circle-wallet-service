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
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	Status         TxnStatus `json:"status"`
	TxnHash        string    `json:"txn_hash,omitempty"`
	SenderAddress  string    `json:"sender_address"`
	FunctionID     string    `json:"function_id"`
	WalletID       string    `json:"wallet_id"`
	// PayloadJSON holds QueuedPayload (type_arguments, arguments, optional inline wallet) while status is queued/processing.
	PayloadJSON string `json:"payload_json,omitempty"`
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
	TypeArguments []string     `json:"type_arguments"`
	Arguments     []any        `json:"arguments"`
	WalletID      string       `json:"wallet_id,omitempty"`
	Wallet        *WalletField `json:"wallet,omitempty"`
}

// WalletField matches execute request inline wallet (public for JSON).
type WalletField struct {
	WalletID  string `json:"wallet_id"`
	Address   string `json:"address"`
	PublicKey string `json:"public_key"`
}

type Store interface {
	Create(ctx context.Context, rec *TransactionRecord) error
	Update(ctx context.Context, rec *TransactionRecord) error
	Get(ctx context.Context, id string) (*TransactionRecord, error)
	GetByIdempotencyKey(ctx context.Context, key string) (*TransactionRecord, error)
	ListByStatus(ctx context.Context, status TxnStatus) ([]*TransactionRecord, error)
	Close() error
}

// Queue extends Store with claim and sequence helpers for the submitter worker (MySQL).
type Queue interface {
	Store
	// ClaimNextQueued locks the oldest queued transaction (by created_at, id), sets status=processing,
	// locks account_sequences for that sender, and returns the row plus the current next_sequence from DB (before chain reconcile).
	ClaimNextQueued(ctx context.Context) (*TransactionRecord, uint64, error)
	// UpsertNextSequence sets the expected next sequence for a sender after a successful submit (usedSeq+1).
	UpsertNextSequence(ctx context.Context, senderAddress string, next uint64) error
	// RecoverStaleProcessing moves stale processing rows back to queued (worker crash recovery).
	RecoverStaleProcessing(ctx context.Context, olderThan time.Duration) (int64, error)
}
