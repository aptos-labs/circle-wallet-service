package webhook

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// newTestWorker creates a Worker with a plain HTTP client (no SSRF dialer) for httptest usage.
func newTestWorker(ws WebhookStore, maxRetries int, timeout time.Duration, logger *slog.Logger) *Worker {
	w := NewWorker(ws, maxRetries, timeout, logger)
	w.httpClient = &http.Client{Timeout: timeout}
	return w
}

type mockWorkerStore struct {
	mu        sync.Mutex
	claim     []*DeliveryRecord
	updates   []*DeliveryRecord
	updateErr error
}

func (m *mockWorkerStore) CreateDelivery(context.Context, *DeliveryRecord) error {
	return nil
}

func (m *mockWorkerStore) ClaimPendingDeliveries(_ context.Context, _ int) ([]*DeliveryRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.claim, nil
}

func (m *mockWorkerStore) UpdateDelivery(_ context.Context, rec *DeliveryRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateErr != nil {
		return m.updateErr
	}
	cp := *rec
	m.updates = append(m.updates, &cp)
	return nil
}

func (m *mockWorkerStore) ListByTransactionID(context.Context, string) ([]*DeliveryRecord, error) {
	return nil, nil
}

func (m *mockWorkerStore) RecoverStaleDeliveries(_ context.Context, _ time.Duration) (int64, error) {
	return 0, nil
}

func TestDeliverSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := &DeliveryRecord{
		ID:            "d1",
		TransactionID: "t1",
		URL:           srv.URL,
		Payload:       `{}`,
		Status:        "pending",
		NextRetryAt:   time.Now().UTC(),
		CreatedAt:     time.Now().UTC(),
	}
	ms := &mockWorkerStore{}
	w := newTestWorker(ms, 5, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.deliver(context.Background(), rec)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.updates) != 1 {
		t.Fatalf("updates: got %d, want 1", len(ms.updates))
	}
	u := ms.updates[0]
	if u.Status != "delivered" {
		t.Errorf("status = %q, want delivered", u.Status)
	}
	if u.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", u.Attempts)
	}
}

func TestDeliver4xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	rec := &DeliveryRecord{
		ID:            "d2",
		TransactionID: "t2",
		URL:           srv.URL,
		Payload:       `{}`,
		Status:        "pending",
		NextRetryAt:   time.Now().UTC(),
		CreatedAt:     time.Now().UTC(),
	}
	ms := &mockWorkerStore{}
	w := newTestWorker(ms, 5, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.deliver(context.Background(), rec)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.updates) != 1 {
		t.Fatalf("updates: got %d", len(ms.updates))
	}
	u := ms.updates[0]
	if u.Status != "failed" {
		t.Errorf("status = %q, want failed", u.Status)
	}
	if u.Attempts != 1 {
		t.Errorf("attempts = %d", u.Attempts)
	}
}

func TestDeliver5xxRetry(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	before := time.Now().UTC()
	rec := &DeliveryRecord{
		ID:            "d3",
		TransactionID: "t3",
		URL:           srv.URL,
		Payload:       `{}`,
		Status:        "pending",
		Attempts:      0,
		NextRetryAt:   before,
		CreatedAt:     before,
	}
	ms := &mockWorkerStore{}
	w := newTestWorker(ms, 10, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.deliver(context.Background(), rec)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.updates) != 1 {
		t.Fatalf("updates: got %d", len(ms.updates))
	}
	u := ms.updates[0]
	if u.Status != "pending" {
		t.Errorf("status = %q, want pending", u.Status)
	}
	if u.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", u.Attempts)
	}
	if !u.NextRetryAt.After(before) {
		t.Errorf("NextRetryAt = %v, want after %v", u.NextRetryAt, before)
	}
}

func TestDeliverMaxRetriesExhausted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rec := &DeliveryRecord{
		ID:            "d4",
		TransactionID: "t4",
		URL:           srv.URL,
		Payload:       `{}`,
		Status:        "pending",
		Attempts:      2,
		NextRetryAt:   time.Now().UTC(),
		CreatedAt:     time.Now().UTC(),
	}
	ms := &mockWorkerStore{}
	w := newTestWorker(ms, 3, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.deliver(context.Background(), rec)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.updates) != 1 {
		t.Fatalf("updates: got %d", len(ms.updates))
	}
	u := ms.updates[0]
	if u.Status != "failed" {
		t.Errorf("status = %q, want failed", u.Status)
	}
	if u.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", u.Attempts)
	}
}

func TestProcessBatch(t *testing.T) {
	t.Parallel()
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()
	bad4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer bad4.Close()
	retrySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer retrySrv.Close()

	now := time.Now().UTC()
	ms := &mockWorkerStore{
		claim: []*DeliveryRecord{
			{ID: "a1", TransactionID: "t1", URL: okSrv.URL, Payload: `{}`, Status: "pending", NextRetryAt: now, CreatedAt: now},
			{ID: "a2", TransactionID: "t2", URL: bad4.URL, Payload: `{}`, Status: "pending", NextRetryAt: now, CreatedAt: now},
			{ID: "a3", TransactionID: "t3", URL: retrySrv.URL, Payload: `{}`, Status: "pending", NextRetryAt: now, CreatedAt: now},
		},
	}
	w := newTestWorker(ms, 10, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.processBatch(context.Background())

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.updates) != 3 {
		t.Fatalf("updates: got %d, want 3", len(ms.updates))
	}
	byID := make(map[string]*DeliveryRecord)
	for _, u := range ms.updates {
		byID[u.ID] = u
	}
	if byID["a1"].Status != "delivered" {
		t.Errorf("a1 status=%q", byID["a1"].Status)
	}
	if byID["a2"].Status != "failed" {
		t.Errorf("a2 status=%q", byID["a2"].Status)
	}
	if byID["a3"].Status != "pending" || byID["a3"].Attempts != 1 {
		t.Errorf("a3 status=%q attempts=%d", byID["a3"].Status, byID["a3"].Attempts)
	}
}

func TestUpdateDeliveryErrorStillAttemptsDeliver(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	now := time.Now().UTC()
	rec := &DeliveryRecord{
		ID: "d-upd-err", TransactionID: "t", URL: srv.URL, Payload: `{}`,
		Status: "pending", NextRetryAt: now, CreatedAt: now,
	}
	ms := &mockWorkerStore{updateErr: errors.New("db write failed")}
	w := newTestWorker(ms, 5, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.deliver(context.Background(), rec)
	ms.mu.Lock()
	n := len(ms.updates)
	ms.mu.Unlock()
	if n != 0 {
		t.Fatalf("expected no successful updates, got %d", n)
	}
}

func TestWorkerConcurrency(t *testing.T) {
	t.Parallel()
	ms := &mockWorkerStore{claim: nil}
	w := newTestWorker(ms, 5, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not exit after cancel")
	}
}

func TestDeliverNetworkError(t *testing.T) {
	t.Parallel()
	rec := &DeliveryRecord{
		ID:            "d5",
		TransactionID: "t5",
		URL:           "http://127.0.0.1:65431",
		Payload:       `{}`,
		Status:        "pending",
		Attempts:      0,
		NextRetryAt:   time.Now().UTC(),
		CreatedAt:     time.Now().UTC(),
	}
	ms := &mockWorkerStore{}
	w := newTestWorker(ms, 5, 2*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	before := time.Now().UTC()
	w.deliver(context.Background(), rec)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.updates) != 1 {
		t.Fatalf("updates: got %d", len(ms.updates))
	}
	u := ms.updates[0]
	if u.Status != "pending" {
		t.Errorf("status = %q, want pending", u.Status)
	}
	if u.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", u.Attempts)
	}
	if !u.NextRetryAt.After(before) {
		t.Errorf("NextRetryAt = %v, want after %v", u.NextRetryAt, before)
	}
}
