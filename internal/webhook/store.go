package webhook

import (
	"context"
	"time"
)

type DeliveryRecord struct {
	ID            string
	TransactionID string
	URL           string
	Payload       string // JSON
	Status        string // pending, delivering, delivered, failed
	Attempts      int
	LastAttemptAt *time.Time
	LastError     string
	NextRetryAt   time.Time
	CreatedAt     time.Time
}

type WebhookStore interface {
	CreateDelivery(ctx context.Context, rec *DeliveryRecord) error
	ClaimPendingDeliveries(ctx context.Context, limit int) ([]*DeliveryRecord, error)
	UpdateDelivery(ctx context.Context, rec *DeliveryRecord) error
	ListByTransactionID(ctx context.Context, txnID string) ([]*DeliveryRecord, error)
	RecoverStaleDeliveries(ctx context.Context, olderThan time.Duration) (int64, error)
}
