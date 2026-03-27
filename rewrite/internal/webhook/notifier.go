package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aptos-labs/jc-contract-integration/rewrite/internal/store"
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
	globalURL  string
	httpClient *http.Client
	logger     *slog.Logger
}

func NewNotifier(globalURL string, logger *slog.Logger) *Notifier {
	return &Notifier{
		globalURL:  globalURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		logger:     logger,
	}
}

// Notify sends the webhook payload async. Per-transaction URL takes precedence over global.
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
	go n.send(url, body, rec.ID)
}

func (n *Notifier) send(url string, body []byte, txnID string) {
	const maxRetries = 3
	for attempt := range maxRetries {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			cancel()
			n.logger.Error("webhook request build failed", "txn_id", txnID, "error", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := n.httpClient.Do(req)
		cancel()
		if err != nil {
			n.logger.Warn("webhook delivery failed", "txn_id", txnID, "attempt", attempt+1, "error", err)
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			n.logger.Info("webhook delivered", "txn_id", txnID, "status", resp.StatusCode)
			return
		}
		n.logger.Warn("webhook non-2xx", "txn_id", txnID, "status", resp.StatusCode, "attempt", attempt+1)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return // don't retry client errors
		}
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	n.logger.Error("webhook retries exhausted", "txn_id", txnID, "url", url,
		"error", fmt.Sprintf("failed after %d attempts", maxRetries))
}
