package store

import (
	"context"
	"time"
)

// TxnStatus represents the lifecycle state of a tracked transaction.
type TxnStatus string

const (
	StatusPending           TxnStatus = "pending"
	StatusSubmitted         TxnStatus = "submitted"
	StatusConfirmed         TxnStatus = "confirmed"
	StatusFailed            TxnStatus = "failed"
	StatusExpired           TxnStatus = "expired"
	StatusPermanentlyFailed TxnStatus = "permanently_failed"
)

// TransactionRecord tracks a blockchain transaction through its lifecycle.
type TransactionRecord struct {
	ID             string    `json:"id"`
	OperationType  string    `json:"operation_type"`
	Status         TxnStatus `json:"status"`
	TxnHash        string    `json:"txn_hash,omitempty"`
	Nonce          uint64    `json:"nonce"`
	SenderAddress  string    `json:"sender_address"`
	Attempt        int       `json:"attempt"`
	MaxRetries     int       `json:"max_retries"`
	RequestPayload string    `json:"request_payload"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	RetryAfter     time.Time `json:"retry_after,omitempty"`
}

// Store defines the persistence interface for transaction tracking.
type Store interface {
	CreateTransaction(ctx context.Context, rec *TransactionRecord) error
	UpdateTransaction(ctx context.Context, rec *TransactionRecord) error
	GetTransaction(ctx context.Context, id string) (*TransactionRecord, error)
	ListByStatus(ctx context.Context, status TxnStatus, limit int) ([]*TransactionRecord, error)
	ListRetryable(ctx context.Context, limit int) ([]*TransactionRecord, error)
	ListPendingRetries(ctx context.Context, limit int) ([]*TransactionRecord, error)
	Close() error
}
