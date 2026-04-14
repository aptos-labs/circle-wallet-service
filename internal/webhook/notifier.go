package webhook

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/google/uuid"
)

type Payload struct {
	TransactionID string          `json:"transaction_id"`
	Status        store.TxnStatus `json:"status"`
	TxnHash       string          `json:"txn_hash,omitempty"`
	ErrorMessage  string          `json:"error_message,omitempty"`
	SenderAddress string          `json:"sender_address"`
	FunctionID    string          `json:"function_id"`
	Timestamp     time.Time       `json:"timestamp"`
}

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

func (n *Notifier) Notify(rec *store.TransactionRecord) {
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
	if err := n.store.CreateDelivery(context.Background(), delivery); err != nil {
		n.logger.Error("webhook outbox insert failed", "txn_id", rec.ID, "error", err)
	}
}
