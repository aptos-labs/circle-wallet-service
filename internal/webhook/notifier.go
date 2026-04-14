// Package webhook implements the persistent outbox pattern for webhook delivery.
//
// When a transaction reaches a terminal status, [Notifier.Notify] inserts a
// [DeliveryRecord] into MySQL. The background [Worker] claims pending records,
// POSTs the JSON payload to the target URL, and retries with exponential backoff
// on transient failures (5xx, network errors, 408, 429). Permanent client errors
// (other 4xx) are not retried.
//
// The Worker also recovers deliveries orphaned in "delivering" state for more
// than 5 minutes (e.g. after a crash) by resetting them to "pending".
package webhook

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/google/uuid"
)

// Payload is the JSON body POSTed to webhook URLs on terminal transaction status.
type Payload struct {
	TransactionID string          `json:"transaction_id"`
	Status        store.TxnStatus `json:"status"`
	TxnHash       string          `json:"txn_hash,omitempty"`
	ErrorMessage  string          `json:"error_message,omitempty"`
	SenderAddress string          `json:"sender_address"`
	FunctionID    string          `json:"function_id"`
	Timestamp     time.Time       `json:"timestamp"`
}

// Notifier writes delivery records to the persistent outbox. It resolves the
// webhook URL (per-request URL takes precedence over globalURL) and inserts a
// pending [DeliveryRecord] for the [Worker] to pick up.
type Notifier struct {
	globalURL string
	store     WebhookStore
	logger    *slog.Logger
}

func NewNotifier(globalURL string, ws WebhookStore, logger *slog.Logger) *Notifier {
	return &Notifier{
		globalURL: globalURL,
		store:     ws,
		logger:    logger,
	}
}

func (n *Notifier) Notify(ctx context.Context, rec *store.TransactionRecord) {
	url := rec.WebhookURL
	if url == "" {
		url = n.globalURL
	}
	if url == "" {
		return
	}

	payload := Payload{
		TransactionID: rec.ID,
		Status:        rec.Status,
		TxnHash:       rec.TxnHash,
		ErrorMessage:  rec.ErrorMessage,
		SenderAddress: rec.SenderAddress,
		FunctionID:    rec.FunctionID,
		Timestamp:     time.Now().UTC(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		n.logger.Error("webhook marshal failed", "txn_id", rec.ID, "error", err)
		return
	}

	now := time.Now().UTC()
	delivery := &DeliveryRecord{
		ID:            uuid.New().String(),
		TransactionID: rec.ID,
		URL:           url,
		Payload:       string(body),
		Status:        "pending",
		NextRetryAt:   now,
		CreatedAt:     now,
	}
	if err := n.store.CreateDelivery(ctx, delivery); err != nil {
		n.logger.Error("webhook outbox insert failed", "txn_id", rec.ID, "error", err)
	}
}
