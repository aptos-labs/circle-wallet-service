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
//
// FailureKind and VmStatus are populated only for failed transactions.
// FailureKind is a coarse taxonomy ("simulation", "submit", "expired",
// "validation", "signing", "assembly") so consumers can route programmatically
// without parsing error_message. VmStatus carries the Aptos VM's structured
// rejection reason (e.g. "Move abort … EINSUFFICIENT_BALANCE") and is only set
// when the failure came from pre-submit simulation.
type Payload struct {
	TransactionID string          `json:"transaction_id"`
	Status        store.TxnStatus `json:"status"`
	TxnHash       string          `json:"txn_hash,omitempty"`
	ErrorMessage  string          `json:"error_message,omitempty"`
	FailureKind   string          `json:"failure_kind,omitempty"`
	VmStatus      string          `json:"vm_status,omitempty"`
	SenderAddress string          `json:"sender_address"`
	FunctionID    string          `json:"function_id"`
	Timestamp     time.Time       `json:"timestamp"`
}

type Notifier interface {
	// Notify sends a message to a consumer
	Notify(ctx context.Context, rec *store.TransactionRecord)
}

type NoopNotifier struct{}

func NewNoopNotifier() Notifier {
	return &NoopNotifier{}
}

// Notify does nothing
func (n *NoopNotifier) Notify(_ context.Context, _ *store.TransactionRecord) {}

type DebugNotifier struct {
	logger *slog.Logger
}

func NewDebugNotifier(logger *slog.Logger) Notifier {
	return &DebugNotifier{
		logger: logger,
	}
}

// Notify sends a message to a log
func (n *DebugNotifier) Notify(_ context.Context, rec *store.TransactionRecord) {
	n.logger.Debug("debug notifier", "txn", rec)
}

// WebHookNotifier writes delivery records to the persistent outbox. It resolves the
// webhook URL (per-request URL) and inserts a
// pending [DeliveryRecord] for the [Worker] to pick up.
type WebHookNotifier struct {
	globalUrl string
	store     WebhookStore
	logger    *slog.Logger
}

func NewWebhookNotifier(globalUrl string, ws WebhookStore, logger *slog.Logger) Notifier {
	return &WebHookNotifier{
		globalUrl: globalUrl,
		store:     ws,
		logger:    logger,
	}
}

// Notify sends a message to the webhook consumer
func (n *WebHookNotifier) Notify(ctx context.Context, rec *store.TransactionRecord) {
	url := rec.WebhookURL

	if url == "" {
		if n.globalUrl != "" {
			// Use global if available
			url = n.globalUrl
		} else {
			// Quit early if no webhook
			return
		}
	}

	payload := Payload{
		TransactionID: rec.ID,
		Status:        rec.Status,
		TxnHash:       rec.TxnHash,
		ErrorMessage:  rec.ErrorMessage,
		FailureKind:   rec.FailureKind,
		VmStatus:      rec.VmStatus,
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
