package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/config"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

func testConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:        8080,
			TestingMode: true,
		},
		MySQL: config.MySQLConfig{DSN: "user:pass@tcp(127.0.0.1:3306)/test"},
		Aptos: config.AptosConfig{
			NodeURL: "https://api.testnet.aptoslabs.com/v1",
			ChainID: 2,
		},
		Transaction: config.TransactionConfig{
			MaxGasAmount:      2000000,
			ExpirationSeconds: 3600,
		},
		Submitter: config.SubmitterConfig{
			PollIntervalMs:          200,
			MaxRetryDurationSeconds: 300,
			RetryIntervalSeconds:    5,
			RetryJitterSeconds:      2,
			StaleProcessingSeconds:  120,
			RecoveryTickSeconds:     30,
			SigningPipelineDepth:    4,
		},
		Poller: config.PollerConfig{IntervalSeconds: 5},
		Webhook: config.WebhookConfig{
			MaxRetries:     5,
			TimeoutSeconds: 10,
		},
		RateLimit: config.RateLimitConfig{
			Enabled:           false,
			RequestsPerSecond: 100,
			Burst:             200,
		},
	}
}

func newTestMemoryStore(t *testing.T) *store.MemoryStore {
	t.Helper()
	s := store.NewMemoryStore(24 * time.Hour)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

type idempLookupFailStore struct {
	*store.MemoryStore
}

func (s *idempLookupFailStore) GetByIdempotencyKey(ctx context.Context, key string) (*store.TransactionRecord, error) {
	if key != "" {
		return nil, errors.New("lookup failed")
	}
	return s.MemoryStore.GetByIdempotencyKey(ctx, key)
}

type createFailStore struct {
	*store.MemoryStore
}

func (s *createFailStore) Create(ctx context.Context, rec *store.TransactionRecord) error {
	return errors.New("create failed")
}

func TestExecute_ValidRequest(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{
		"wallet_id":   "w1",
		"address":     "0x1",
		"function_id": "0x1::module::entry",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	id, _ := resp["transaction_id"].(string)
	if id == "" {
		t.Fatalf("missing transaction_id: %#v", resp)
	}
	if resp["status"] != string(store.StatusQueued) {
		t.Fatalf("status: %#v", resp["status"])
	}
}

func TestExecute_MissingWalletID(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{"address": "0x1", "function_id": "0x1::m::f"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestExecute_MissingAddress(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{"wallet_id": "w", "function_id": "0x1::m::f"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestExecute_MissingFunctionID(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{"wallet_id": "w", "address": "0x1"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestExecute_WithFeePayer(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{
		"wallet_id":   "w1",
		"address":     "0x1",
		"function_id": "0x1::m::f",
		"fee_payer": map[string]any{
			"wallet_id": "fpw",
			"address":   "0x2",
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	id, _ := resp["transaction_id"].(string)
	rec, err := st.Get(req.Context(), id)
	if err != nil || rec == nil {
		t.Fatalf("get record: %v %v", rec, err)
	}
	if rec.FeePayerWalletID != "fpw" || rec.FeePayerAddress == "" {
		t.Fatalf("fee payer fields: wallet=%q addr=%q", rec.FeePayerWalletID, rec.FeePayerAddress)
	}
	var qp store.QueuedPayload
	if err := json.Unmarshal([]byte(rec.PayloadJSON), &qp); err != nil {
		t.Fatal(err)
	}
}

func TestExecute_InvalidFeePayerAddress(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{
		"wallet_id":   "w1",
		"address":     "0x1",
		"function_id": "0x1::m::f",
		"fee_payer": map[string]any{
			"wallet_id": "fpw",
			"address":   "not-a-valid-address",
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestExecute_IdempotencyKeyBody(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	base := map[string]any{
		"wallet_id":       "w1",
		"address":         "0x1",
		"function_id":     "0x1::m::f",
		"idempotency_key": "idem-body-1",
	}
	do := func() *httptest.ResponseRecorder {
		b, _ := json.Marshal(base)
		req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	r1 := do()
	r2 := do()
	if r1.Code != http.StatusAccepted || r2.Code != http.StatusAccepted {
		t.Fatalf("codes %d %d", r1.Code, r2.Code)
	}
	var o1, o2 map[string]any
	_ = json.Unmarshal(r1.Body.Bytes(), &o1)
	_ = json.Unmarshal(r2.Body.Bytes(), &o2)
	if o1["transaction_id"] != o2["transaction_id"] {
		t.Fatalf("ids differ: %v vs %v", o1["transaction_id"], o2["transaction_id"])
	}
	if r2.Header().Get("X-Idempotency-Replayed") != "true" {
		t.Fatalf("replay header: %q", r2.Header().Get("X-Idempotency-Replayed"))
	}
}

func TestExecute_IdempotencyLookupError(t *testing.T) {
	cfg := testConfig()
	base := newTestMemoryStore(t)
	st := &idempLookupFailStore{MemoryStore: base}
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{
		"wallet_id": "w1", "address": "0x1", "function_id": "0x1::m::f",
		"idempotency_key": "k-err",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestExecute_CreateFailure(t *testing.T) {
	cfg := testConfig()
	st := &createFailStore{MemoryStore: newTestMemoryStore(t)}
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{"wallet_id": "w1", "address": "0x1", "function_id": "0x1::m::f"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestExecute_IdempotentReplayWithHashAndSequence(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	now := time.Now().UTC()
	seq := uint64(9)
	rec := &store.TransactionRecord{
		ID:             "existing",
		IdempotencyKey: "idem-replay",
		Status:         store.StatusSubmitted,
		SenderAddress:  "0x0000000000000000000000000000000000000000000000000000000000000001",
		FunctionID:     "0x1::m::f",
		WalletID:       "w1",
		TxnHash:        "0xbeef",
		SequenceNumber: &seq,
		PayloadJSON:    `{}`,
		CreatedAt:      now,
		UpdatedAt:      now,
		ExpiresAt:      now.Add(time.Hour),
	}
	if err := st.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"wallet_id": "w1", "address": "0x1", "function_id": "0x1::m::f",
		"idempotency_key": "idem-replay",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("want 202 got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["txn_hash"] != "0xbeef" {
		t.Fatalf("txn_hash: %#v", resp["txn_hash"])
	}
	if resp["sequence_number"] != float64(9) {
		t.Fatalf("sequence_number: %#v", resp["sequence_number"])
	}
}

func TestExecute_UnknownJSONField(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader([]byte(
		`{"wallet_id":"w1","address":"0x1","function_id":"0x1::m::f","not_allowed":true}`,
	)))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestExecute_FeePayerMissingWalletID(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{
		"wallet_id": "w1", "address": "0x1", "function_id": "0x1::m::f",
		"fee_payer": map[string]any{"address": "0x2"},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestExecute_FeePayerMissingAddress(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{
		"wallet_id": "w1", "address": "0x1", "function_id": "0x1::m::f",
		"fee_payer": map[string]any{"wallet_id": "fp"},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestExecute_InvalidJSON(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader([]byte(`{"wallet_id":`)))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestExecute_InvalidAddress(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{
		"wallet_id":   "w1",
		"address":     "not-hex",
		"function_id": "0x1::m::f",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestExecute_WithTypeArguments(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{
		"wallet_id":      "w1",
		"address":        "0x1",
		"function_id":    "0x1::m::f",
		"type_arguments": []string{"0x1::coin::CoinStore<0x1::aptos_coin::AptosCoin>"},
		"arguments":      []any{float64(1), "0x1"},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	id, _ := resp["transaction_id"].(string)
	rec, err := st.Get(req.Context(), id)
	if err != nil || rec == nil {
		t.Fatalf("get: %v %v", rec, err)
	}
	var qp store.QueuedPayload
	if err := json.Unmarshal([]byte(rec.PayloadJSON), &qp); err != nil {
		t.Fatal(err)
	}
	if len(qp.TypeArguments) != 1 || qp.TypeArguments[0] != "0x1::coin::CoinStore<0x1::aptos_coin::AptosCoin>" {
		t.Fatalf("type_arguments: %#v", qp.TypeArguments)
	}
	if len(qp.Arguments) != 2 {
		t.Fatalf("arguments: %#v", qp.Arguments)
	}
}

func TestExecute_ExpiresAtFromConfig(t *testing.T) {
	cfg := testConfig()
	cfg.Transaction.ExpirationSeconds = 7200
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	body := map[string]any{"wallet_id": "w1", "address": "0x1", "function_id": "0x1::m::f"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	id, _ := resp["transaction_id"].(string)
	rec, err := st.Get(req.Context(), id)
	if err != nil || rec == nil {
		t.Fatal(err)
	}
	want := rec.CreatedAt.Add(7200 * time.Second)
	if rec.ExpiresAt.Sub(want).Abs() > time.Second {
		t.Fatalf("ExpiresAt=%v want~%v", rec.ExpiresAt, want)
	}
}

func TestExecute_IdempotencyKeyHeader(t *testing.T) {
	cfg := testConfig()
	st := newTestMemoryStore(t)
	h := Execute(cfg, st, slog.New(slog.DiscardHandler))

	base := map[string]any{
		"wallet_id":   "w1",
		"address":     "0x1",
		"function_id": "0x1::m::f",
	}
	do := func() *httptest.ResponseRecorder {
		b, _ := json.Marshal(base)
		req := httptest.NewRequest(http.MethodPost, "/v1/execute", bytes.NewReader(b))
		req.Header.Set("Idempotency-Key", "idem-hdr-1")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	r1 := do()
	r2 := do()
	if r1.Code != http.StatusAccepted || r2.Code != http.StatusAccepted {
		t.Fatalf("codes %d %d", r1.Code, r2.Code)
	}
	var o1, o2 map[string]any
	_ = json.Unmarshal(r1.Body.Bytes(), &o1)
	_ = json.Unmarshal(r2.Body.Bytes(), &o2)
	if o1["transaction_id"] != o2["transaction_id"] {
		t.Fatalf("ids differ: %v vs %v", o1["transaction_id"], o2["transaction_id"])
	}
	if r2.Header().Get("X-Idempotency-Replayed") != "true" {
		t.Fatalf("replay header: %q", r2.Header().Get("X-Idempotency-Replayed"))
	}
}
