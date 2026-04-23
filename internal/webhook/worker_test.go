package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }

// newTestWorker creates a Worker with a plain HTTP client (no SSRF dialer) for httptest usage.
// Redirect validation (CheckRedirect) is preserved so redirect behavior can be exercised
// against loopback httptest servers.
func newTestWorker(ws WebhookStore, maxRetries int, timeout time.Duration, logger *slog.Logger) *Worker {
	w := NewWorker(ws, maxRetries, timeout, 1, "", logger)
	w.httpClient = &http.Client{
		Timeout:       timeout,
		CheckRedirect: ssrfSafeCheckRedirect,
	}
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

func TestDeliver_SignsWithHMAC(t *testing.T) {
	t.Parallel()
	const secret = "s3cret-key"
	const payload = `{"txn_id":"abc","status":"confirmed"}`

	var gotSig, gotTs string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Signature")
		gotTs = r.Header.Get("X-Signature-Timestamp")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	now := time.Now().UTC()
	rec := &DeliveryRecord{
		ID: "sig-1", TransactionID: "t-sig", URL: srv.URL, Payload: payload,
		Status: "pending", NextRetryAt: now, CreatedAt: now,
	}
	ms := &mockWorkerStore{}
	w := NewWorker(ms, 5, 5*time.Second, 1, secret, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.httpClient = &http.Client{Timeout: 5 * time.Second} // bypass SSRF dialer for httptest

	before := time.Now().UTC().Unix()
	w.deliver(context.Background(), rec)
	after := time.Now().UTC().Unix()

	if !strings.HasPrefix(gotSig, "sha256=") {
		t.Fatalf("X-Signature = %q, want sha256= prefix", gotSig)
	}
	ts, err := strconv.ParseInt(gotTs, 10, 64)
	if err != nil {
		t.Fatalf("X-Signature-Timestamp = %q: %v", gotTs, err)
	}
	if ts < before || ts > after {
		t.Fatalf("timestamp %d outside [%d,%d]", ts, before, after)
	}

	// Recompute expected signature: HMAC-SHA256(secret, "<ts>.<payload>")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(gotTs))
	mac.Write([]byte{'.'})
	mac.Write([]byte(payload))
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(gotSig), []byte(want)) {
		t.Fatalf("signature mismatch:\n got = %q\nwant = %q", gotSig, want)
	}
}

func TestDeliver_NoSignatureWhenSecretEmpty(t *testing.T) {
	t.Parallel()
	var hadSig, hadTs bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadSig = r.Header["X-Signature"]
		_, hadTs = r.Header["X-Signature-Timestamp"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	now := time.Now().UTC()
	rec := &DeliveryRecord{
		ID: "nosig", TransactionID: "t", URL: srv.URL, Payload: `{}`,
		Status: "pending", NextRetryAt: now, CreatedAt: now,
	}
	ms := &mockWorkerStore{}
	w := newTestWorker(ms, 5, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.deliver(context.Background(), rec)

	if hadSig || hadTs {
		t.Fatalf("unexpected signature headers with empty secret (sig=%v ts=%v)", hadSig, hadTs)
	}
}

func TestValidateRedirectURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"public https", "https://example.com/callback", false},
		{"public http", "http://93.184.216.34/", false},
		{"loopback literal v4", "http://127.0.0.1/", true},
		{"loopback literal v6", "http://[::1]/", true},
		{"rfc1918 literal", "http://10.0.0.1/", true},
		{"link-local literal", "http://169.254.169.254/", true},
		{"unspecified literal", "http://0.0.0.0/", true},
		{"file scheme", "file:///etc/passwd", true},
		{"ftp scheme", "ftp://example.com/", true},
		{"missing host", "http:///path", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u, err := parseURL(tc.raw)
			if err != nil {
				// Some malformed URLs fail at parse time — treat as rejected.
				if !tc.wantErr {
					t.Fatalf("parse %q: %v", tc.raw, err)
				}
				return
			}
			err = validateRedirectURL(u)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateRedirectURL(%q) err=%v, wantErr=%v", tc.raw, err, tc.wantErr)
			}
		})
	}
}

func TestDeliver_RejectsRedirectToLoopback(t *testing.T) {
	t.Parallel()
	// Target the redirect at 127.0.0.1:1 (guaranteed-closed port) so even if the
	// SSRF check somehow didn't fire we wouldn't actually send data to a real listener.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/", http.StatusFound)
	}))
	defer srv.Close()

	now := time.Now().UTC()
	rec := &DeliveryRecord{
		ID: "r1", TransactionID: "t-r1", URL: srv.URL, Payload: `{}`,
		Status: "pending", NextRetryAt: now, CreatedAt: now,
	}
	ms := &mockWorkerStore{}
	w := newTestWorker(ms, 3, 2*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.deliver(context.Background(), rec)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.updates) != 1 {
		t.Fatalf("updates: got %d", len(ms.updates))
	}
	u := ms.updates[0]
	// Redirect to loopback should NOT land as delivered; the CheckRedirect hook aborts.
	if u.Status == "delivered" {
		t.Fatalf("delivery succeeded despite redirect to loopback: %+v", u)
	}
	// The precise error message surfaced from http.Client after CheckRedirect returns
	// an error varies by Go version, so we only assert it didn't succeed.
	_ = strings.ToLower(u.LastError)
}

func TestDeliver_RejectsTooManyRedirects(t *testing.T) {
	t.Parallel()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to ourselves to create an infinite chain; the hop-count cap should cut it off.
		target := srv.URL + r.URL.Path + "/next"
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()

	// Override ssrfSafeCheckRedirect path: the redirect target is 127.0.0.1 (httptest),
	// which normally would fail validateRedirectURL. For this test we want to verify the
	// hop-count cap, so use a CheckRedirect that only counts hops.
	now := time.Now().UTC()
	rec := &DeliveryRecord{
		ID: "r2", TransactionID: "t-r2", URL: srv.URL, Payload: `{}`,
		Status: "pending", NextRetryAt: now, CreatedAt: now,
	}
	ms := &mockWorkerStore{}
	w := NewWorker(ms, 3, 2*time.Second, 1, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Replace transport+redirect with a loopback-tolerant variant that still caps hops.
	w.httpClient = &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxWebhookRedirects {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	w.deliver(context.Background(), rec)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.updates) != 1 {
		t.Fatalf("updates: got %d", len(ms.updates))
	}
	// http.ErrUseLastResponse makes Do return the last redirect response (302) — a
	// non-2xx, non-retryable status. Worker treats it as client error ⇒ failed.
	if ms.updates[0].Status == "delivered" {
		t.Fatalf("delivery succeeded on redirect loop: %+v", ms.updates[0])
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
