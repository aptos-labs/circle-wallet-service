package store

import (
	"context"
	"fmt"
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
	ReplayNonce    string    `json:"replay_nonce,omitempty"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	WebhookURL     string    `json:"webhook_url,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// FormatNonce converts a uint64 nonce to its string representation for
// safe JSON serialization (avoids JavaScript number precision loss).
func FormatNonce(n uint64) string {
	return fmt.Sprintf("%d", n)
}

type Store interface {
	Create(ctx context.Context, rec *TransactionRecord) error
	Update(ctx context.Context, rec *TransactionRecord) error
	Get(ctx context.Context, id string) (*TransactionRecord, error)
	GetByIdempotencyKey(ctx context.Context, key string) (*TransactionRecord, error)
	ListByStatus(ctx context.Context, status TxnStatus) ([]*TransactionRecord, error)
	Close() error
}
