package store

import (
	"context"
	"time"
)

type TxnStatus string

const (
	StatusPending   TxnStatus = "pending"
	StatusSubmitted TxnStatus = "submitted"
	StatusConfirmed TxnStatus = "confirmed"
	StatusFailed    TxnStatus = "failed"
	StatusExpired   TxnStatus = "expired"
)

type TransactionRecord struct {
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	Status         TxnStatus `json:"status"`
	TxnHash        string    `json:"txn_hash,omitempty"`
	SenderAddress  string    `json:"sender_address"`
	FunctionID     string    `json:"function_id"`
	WalletID       string    `json:"wallet_id"`
	Orderless      bool      `json:"orderless"`
	ReplayNonce    *uint64   `json:"replay_nonce,omitempty"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	WebhookURL     string    `json:"webhook_url,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type Store interface {
	Create(ctx context.Context, rec *TransactionRecord) error
	Update(ctx context.Context, rec *TransactionRecord) error
	Get(ctx context.Context, id string) (*TransactionRecord, error)
	GetByIdempotencyKey(ctx context.Context, key string) (*TransactionRecord, error)
	ListByStatus(ctx context.Context, status TxnStatus) ([]*TransactionRecord, error)
	Close() error
}
