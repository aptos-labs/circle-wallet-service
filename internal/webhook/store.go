package webhook

import (
	"context"
	"time"
)

// DeliveryRecord represents a single webhook delivery attempt stored in the
// persistent outbox. The background [Worker] claims pending records, attempts
// HTTP delivery, and updates the status with retry/backoff metadata.
type DeliveryRecord struct {
	ID            string
	TransactionID string
	URL           string
	Payload       string // JSON
	Status        string // pending → delivering → delivered | failed
	Attempts      int
	LastAttemptAt *time.Time
	LastError     string
	NextRetryAt   time.Time
	CreatedAt     time.Time
}

// WebhookStore persists webhook delivery records for the outbox pattern.
// The [Notifier] writes new records; the [Worker] claims and delivers them.
type WebhookStore interface {
	CreateDelivery(ctx context.Context, rec *DeliveryRecord) error
	// ClaimPendingDeliveries atomically transitions up to limit pending records
	// whose NextRetryAt has passed to "delivering" and returns them.
	ClaimPendingDeliveries(ctx context.Context, limit int) ([]*DeliveryRecord, error)
	UpdateDelivery(ctx context.Context, rec *DeliveryRecord) error
	ListByTransactionID(ctx context.Context, txnID string) ([]*DeliveryRecord, error)
	// RecoverStaleDeliveries resets records stuck in "delivering" longer than
	// olderThan back to "pending" (e.g. after a worker crash).
	RecoverStaleDeliveries(ctx context.Context, olderThan time.Duration) (int64, error)
}
