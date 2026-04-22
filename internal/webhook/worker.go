package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Worker is the background goroutine that delivers webhooks from the outbox.
// It uses an HTTP client with SSRF-safe dialing (rejects private/loopback IPs).
type Worker struct {
	store         WebhookStore
	httpClient    *http.Client
	maxRetries    int
	signingSecret []byte // HMAC-SHA256 key; nil disables signing
	logger        *slog.Logger
}

// NewWorker builds a Worker. signingSecret is the shared HMAC-SHA256 key used
// to sign outbound deliveries; when empty, deliveries are sent without a
// signature header (useful for dev/tests).
func NewWorker(ws WebhookStore, maxRetries int, timeout time.Duration, signingSecret string, logger *slog.Logger) *Worker {
	transport := &http.Transport{
		DialContext: ssrfSafeDialer(timeout),
	}
	var secret []byte
	if signingSecret != "" {
		secret = []byte(signingSecret)
	}
	return &Worker{
		store:         ws,
		httpClient:    &http.Client{Timeout: timeout, Transport: transport},
		maxRetries:    maxRetries,
		signingSecret: secret,
		logger:        logger,
	}
}

// ssrfSafeDialer returns a DialContext that rejects connections to private/loopback addresses.
func ssrfSafeDialer(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("webhook: invalid address %q: %w", addr, err)
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("webhook: resolve %q: %w", host, err)
		}
		for _, ip := range ips {
			if ip.IP.IsLoopback() || ip.IP.IsPrivate() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() {
				return nil, fmt.Errorf("webhook: refusing to connect to private address %s", ip.IP)
			}
		}
		return dialer.DialContext(ctx, network, addr)
	}
}

// Run starts the delivery loop. It blocks until ctx is cancelled. On each tick
// (1s) it claims up to 10 pending deliveries and attempts delivery. Every 30s
// it also recovers orphaned "delivering" records.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	recoveryTicker := time.NewTicker(30 * time.Second)
	defer recoveryTicker.Stop()

	w.recoverStale(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-recoveryTicker.C:
			w.recoverStale(ctx)
		case <-ticker.C:
			w.processBatch(ctx)
		}
	}
}

const staleDeliveryThreshold = 5 * time.Minute

func (w *Worker) recoverStale(ctx context.Context) {
	n, err := w.store.RecoverStaleDeliveries(ctx, staleDeliveryThreshold)
	if err != nil {
		w.logger.Error("webhook worker: recover stale deliveries", "error", err)
		return
	}
	if n > 0 {
		w.logger.Info("webhook worker: recovered stale delivering rows", "count", n)
	}
}

func (w *Worker) processBatch(ctx context.Context) {
	records, err := w.store.ClaimPendingDeliveries(ctx, 10)
	if err != nil {
		w.logger.Error("webhook worker: claim deliveries", "error", err)
		return
	}
	for _, rec := range records {
		w.deliver(ctx, rec)
	}
}

func (w *Worker) deliver(ctx context.Context, rec *DeliveryRecord) {
	now := time.Now().UTC()
	rec.Attempts++
	rec.LastAttemptAt = &now

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rec.URL, bytes.NewReader([]byte(rec.Payload)))
	if err != nil {
		rec.Status = "failed"
		rec.LastError = fmt.Sprintf("build request: %v", err)
		w.update(ctx, rec)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	w.signRequest(req, rec.Payload)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		w.handleRetry(ctx, rec, fmt.Sprintf("http error: %v", err))
		return
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		rec.Status = "delivered"
		rec.LastError = ""
		w.logger.Info("webhook delivered", "delivery_id", rec.ID, "txn_id", rec.TransactionID)
		w.update(ctx, rec)
		return
	}

	if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests {
		w.handleRetry(ctx, rec, fmt.Sprintf("retryable client error: %d", resp.StatusCode))
		return
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		rec.Status = "failed"
		rec.LastError = fmt.Sprintf("client error: %d", resp.StatusCode)
		w.update(ctx, rec)
		return
	}

	w.handleRetry(ctx, rec, fmt.Sprintf("server error: %d", resp.StatusCode))
}

func (w *Worker) handleRetry(ctx context.Context, rec *DeliveryRecord, errMsg string) {
	rec.LastError = errMsg
	if rec.Attempts >= w.maxRetries {
		rec.Status = "failed"
		w.logger.Error("webhook retries exhausted", "delivery_id", rec.ID, "txn_id", rec.TransactionID)
	} else {
		backoff := math.Pow(2, float64(rec.Attempts))
		if backoff > 300 {
			backoff = 300
		}
		rec.NextRetryAt = time.Now().UTC().Add(time.Duration(backoff) * time.Second)
		rec.Status = "pending"
	}
	w.update(ctx, rec)
}

// signRequest attaches an HMAC-SHA256 signature of "<ts>.<payload>" as
// X-Signature: sha256=<hex>, plus X-Signature-Timestamp. Including the
// timestamp in the signed string lets receivers reject replayed deliveries
// by comparing the header timestamp against their own clock.
// No-op when the worker has no signing secret.
func (w *Worker) signRequest(req *http.Request, payload string) {
	if len(w.signingSecret) == 0 {
		return
	}
	ts := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, w.signingSecret)
	mac.Write([]byte(ts))
	mac.Write([]byte{'.'})
	mac.Write([]byte(payload))
	req.Header.Set("X-Signature-Timestamp", ts)
	req.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
}

func (w *Worker) update(ctx context.Context, rec *DeliveryRecord) {
	if err := w.store.UpdateDelivery(ctx, rec); err != nil {
		w.logger.Error("webhook worker: update delivery", "delivery_id", rec.ID, "error", err)
	}
}
