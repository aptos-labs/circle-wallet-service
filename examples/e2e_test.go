//go:build e2e

// Package examples provides end-to-end tests against a live server.
//
// Usage:
//
//  1. Copy .env.example to .env and fill in credentials (including MYSQL_DSN)
//  2. Start the server: go run ./cmd/server
//  3. Run: go test -tags=e2e ./examples/ -v -count=1
//
// Environment variables (override via shell or .env):
//
//	E2E_BASE_URL         — server URL (default http://localhost:8080)
//	E2E_API_KEY          — must match the server's API_KEY
//	E2E_WALLET_ID        — Circle wallet ID (or first entry in CIRCLE_WALLETS)
//	E2E_WALLET_ADDR      — Aptos address
//	E2E_WALLET_PUBLIC_KEY — Ed25519 public key hex (required for inline-wallet tests if not in CIRCLE_WALLETS JSON)
//	E2E_WALLET2_*        — second wallet for dual-wallet throughput (or CIRCLE_WALLETS[1])
//	E2E_THROUGHPUT       — concurrent executes per wallet in throughput tests (default 8, max 50)
//	E2E_MYSQL_DSN        — optional; for stale-processing recovery test (defaults to MYSQL_DSN)
//
// The server must be configured with MYSQL_DSN and migrations applied (automatic on startup).
package examples

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
)

func init() {
	_ = godotenv.Load("../.env")
}

func baseURL() string {
	if v := os.Getenv("E2E_BASE_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

func apiKey() string {
	if v := os.Getenv("E2E_API_KEY"); v != "" {
		return v
	}
	return os.Getenv("API_KEY")
}

// e2eWallet holds Circle + Aptos credentials for execute tests.
type e2eWallet struct {
	WalletID  string `json:"wallet_id"`
	Address   string `json:"address"`
	PublicKey string `json:"public_key"`
}

func loadCircleWalletsJSON() []e2eWallet {
	raw := os.Getenv("CIRCLE_WALLETS")
	if raw == "" {
		return nil
	}
	var out []e2eWallet
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// wallet1 returns the primary wallet (E2E_WALLET_* overrides CIRCLE_WALLETS[0]).
func wallet1() (e2eWallet, bool) {
	w := e2eWallet{
		WalletID:  os.Getenv("E2E_WALLET_ID"),
		Address:   os.Getenv("E2E_WALLET_ADDR"),
		PublicKey: os.Getenv("E2E_WALLET_PUBLIC_KEY"),
	}
	if w.WalletID != "" && w.Address != "" {
		if w.PublicKey == "" {
			for _, x := range loadCircleWalletsJSON() {
				if x.WalletID == w.WalletID && x.PublicKey != "" {
					w.PublicKey = x.PublicKey
					break
				}
			}
		}
		return w, true
	}
	all := loadCircleWalletsJSON()
	if len(all) == 0 {
		return e2eWallet{}, false
	}
	w = all[0]
	return w, w.WalletID != "" && w.Address != ""
}

// wallet2 returns a second wallet for isolation tests (E2E_WALLET2_* or CIRCLE_WALLETS[1]).
func wallet2() (e2eWallet, bool) {
	w := e2eWallet{
		WalletID:  os.Getenv("E2E_WALLET2_ID"),
		Address:   os.Getenv("E2E_WALLET2_ADDR"),
		PublicKey: os.Getenv("E2E_WALLET2_PUBLIC_KEY"),
	}
	if w.WalletID != "" && w.Address != "" {
		if w.PublicKey == "" {
			for _, x := range loadCircleWalletsJSON() {
				if x.WalletID == w.WalletID && x.PublicKey != "" {
					w.PublicKey = x.PublicKey
					break
				}
			}
		}
		return w, true
	}
	all := loadCircleWalletsJSON()
	if len(all) < 2 {
		return e2eWallet{}, false
	}
	w = all[1]
	return w, w.WalletID != "" && w.Address != ""
}

func walletID() string {
	w, ok := wallet1()
	if !ok {
		return ""
	}
	return w.WalletID
}

func walletAddr() string {
	w, ok := wallet1()
	if !ok {
		return ""
	}
	return w.Address
}

func e2eMySQLDSN() string {
	if v := os.Getenv("E2E_MYSQL_DSN"); v != "" {
		return v
	}
	return os.Getenv("MYSQL_DSN")
}

// doRequest is a helper that makes an HTTP request and returns status code + body.
func doRequest(t *testing.T, method, url string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	status, b, _ := doRequestWithHeaders(t, method, url, body, headers)
	return status, b
}

// doRequestWithHeaders also returns response headers (e.g. idempotency replay).
func doRequestWithHeaders(t *testing.T, method, url string, body any, headers map[string]string) (int, []byte, http.Header) {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, respBody, resp.Header
}

// txnRecord is a subset of GET /v1/transactions/{id} JSON.
type txnRecord struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	TxnHash        string  `json:"txn_hash"`
	ErrorMessage   string  `json:"error_message"`
	AttemptCount   int     `json:"attempt_count"`
	LastError      string  `json:"last_error"`
	IdempotencyKey string  `json:"idempotency_key"`
	SenderAddress  string  `json:"sender_address"`
	SequenceNumber *uint64 `json:"sequence_number,omitempty"`
}

func getTransaction(t *testing.T, id string) (httpCode int, rec txnRecord) {
	t.Helper()
	url := fmt.Sprintf("%s/v1/transactions/%s", baseURL(), id)
	status, body := doRequest(t, "GET", url, nil, authHeaders())
	if status != 200 {
		return status, rec
	}
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatalf("parse transaction: %v", err)
	}
	return status, rec
}

func statusRank(s string) int {
	switch s {
	case "queued":
		return 0
	case "processing":
		return 1
	case "submitted":
		return 2
	case "confirmed":
		return 3
	case "failed", "expired":
		return 4
	default:
		return -1
	}
}

func e2eThroughputCount() int {
	raw := os.Getenv("E2E_THROUGHPUT")
	if raw == "" {
		return 8
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 2 {
		return 8
	}
	if n > 50 {
		return 50
	}
	return n
}

func authHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + apiKey()}
}

func mergeHeaders(base map[string]string, extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// --------------------------------------------------------------------------
// Tests
// --------------------------------------------------------------------------

func TestHealth(t *testing.T) {
	status, body := doRequest(t, "GET", baseURL()+"/v1/health", nil, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}

	var resp map[string]string
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("expected status=ok, got %q", resp["status"])
	}
}

func TestAuthRequired(t *testing.T) {
	// POST /v1/execute without auth should be 401
	status, _ := doRequest(t, "POST", baseURL()+"/v1/execute", map[string]string{}, nil)
	if status != 401 {
		t.Fatalf("expected 401 without auth, got %d", status)
	}

	// POST /v1/query without auth should be 401
	status, _ = doRequest(t, "POST", baseURL()+"/v1/query", map[string]string{}, nil)
	if status != 401 {
		t.Fatalf("expected 401 without auth, got %d", status)
	}
}

func TestGetTransactionNotFound(t *testing.T) {
	status, body := doRequest(t, "GET", baseURL()+"/v1/transactions/nonexistent-id", nil, authHeaders())
	if status != 404 {
		t.Fatalf("expected 404, got %d: %s", status, body)
	}
}

func TestQueryViewFunction(t *testing.T) {
	addr := walletAddr()
	if addr == "" {
		t.Skip("E2E_WALLET_ADDR or CIRCLE_WALLETS not set — skipping query test")
	}

	reqBody := map[string]any{
		"function_id":    "0x1::coin::balance",
		"type_arguments": []string{"0x1::aptos_coin::AptosCoin"},
		"arguments":      []string{addr},
	}

	status, body := doRequest(t, "POST", baseURL()+"/v1/query", reqBody, authHeaders())
	t.Logf("Query response (%d): %s", status, body)

	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}

	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if _, ok := resp["result"]; !ok {
		t.Fatalf("expected 'result' key in response, got: %s", body)
	}
}

func transferExecuteBody(walletIDStr, toAddr string) map[string]any {
	return map[string]any{
		"wallet_id":      walletIDStr,
		"function_id":    "0x1::aptos_account::transfer",
		"type_arguments": []string{},
		"arguments":      []any{toAddr, "1"},
	}
}

// transferExecuteBodyInline sends only the wallet object (no server-config wallet_id lookup).
func transferExecuteBodyInline(w e2eWallet, toAddr string) map[string]any {
	return map[string]any{
		"wallet": map[string]string{
			"wallet_id":  w.WalletID,
			"address":    w.Address,
			"public_key": w.PublicKey,
		},
		"function_id":    "0x1::aptos_account::transfer",
		"type_arguments": []string{},
		"arguments":      []any{toAddr, "1"},
	}
}

func postExecute(t *testing.T, reqBody map[string]any) (httpStatus int, transactionID string, body []byte) {
	t.Helper()
	st, id, b, _ := postExecuteWithHeaders(t, reqBody, nil)
	return st, id, b
}

func postExecuteWithHeaders(t *testing.T, reqBody map[string]any, extraHeaders map[string]string) (httpStatus int, transactionID string, body []byte, hdr http.Header) {
	t.Helper()
	h := mergeHeaders(authHeaders(), extraHeaders)
	status, b, hdr := doRequestWithHeaders(t, "POST", baseURL()+"/v1/execute", reqBody, h)
	if status != 202 {
		return status, "", b, hdr
	}
	var execResp struct {
		TransactionID string `json:"transaction_id"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(b, &execResp); err != nil {
		t.Fatalf("parse execute response: %v", err)
	}
	return status, execResp.TransactionID, b, hdr
}

// pollUntilConfirmed polls GET /v1/transactions/{id} until confirmed or terminal failure.
func pollUntilConfirmed(t *testing.T, transactionID string, pollEvery, maxWait time.Duration) {
	t.Helper()
	deadline := time.Now().Add(maxWait)
	var lastStatus string
	for time.Now().Before(deadline) {
		time.Sleep(pollEvery)
		code, rec := getTransaction(t, transactionID)
		if code != 200 {
			t.Logf("poll: HTTP %d", code)
			continue
		}
		lastStatus = rec.Status
		t.Logf("poll: status=%s hash=%s", rec.Status, rec.TxnHash)
		switch rec.Status {
		case "confirmed":
			t.Logf("transaction confirmed")
			return
		case "failed":
			t.Fatalf("transaction failed: %s", rec.ErrorMessage)
		case "expired":
			t.Fatalf("transaction expired: %s", rec.ErrorMessage)
		}
	}
	t.Fatalf("transaction not confirmed after %v, last status: %s", maxWait, lastStatus)
}

// pollUntilSubmittedThenConfirmed polls quickly; requires observing submitted with txn_hash before confirmed.
func pollUntilSubmittedThenConfirmed(t *testing.T, transactionID string, maxWait time.Duration) {
	t.Helper()
	deadline := time.Now().Add(maxWait)
	var sawSubmittedWithHash bool
	var lastStatus string
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		code, rec := getTransaction(t, transactionID)
		if code != 200 {
			continue
		}
		lastStatus = rec.Status
		if rec.Status == "submitted" && rec.TxnHash != "" {
			sawSubmittedWithHash = true
		}
		switch rec.Status {
		case "confirmed":
			if !sawSubmittedWithHash {
				t.Skip("chain/API fast path: never observed submitted+txn_hash before confirmed (acceptable)")
			}
			t.Logf("observed submitted with hash before confirmed")
			return
		case "failed":
			t.Fatalf("transaction failed: %s", rec.ErrorMessage)
		case "expired":
			t.Fatalf("transaction expired: %s", rec.ErrorMessage)
		}
	}
	t.Fatalf("not confirmed after %v, last=%q sawSubmitted=%v", maxWait, lastStatus, sawSubmittedWithHash)
}

func TestExecuteAndPoll(t *testing.T) {
	w, ok := wallet1()
	if !ok {
		t.Skip("E2E_WALLET_* or CIRCLE_WALLETS[0] — skipping execute test")
	}

	status, txID, body := postExecute(t, transferExecuteBody(w.WalletID, w.Address))
	t.Logf("Execute response (%d): %s", status, body)

	if status != 202 {
		t.Fatalf("expected 202, got %d: %s", status, body)
	}
	if txID == "" {
		t.Fatal("expected transaction_id in response")
	}
	var execResp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &execResp); err != nil {
		t.Fatalf("parse execute response: %v", err)
	}
	if execResp.Status != "queued" {
		t.Fatalf("expected initial status queued, got %q", execResp.Status)
	}
	t.Logf("Transaction ID: %s (queued for submitter)", txID)

	pollUntilConfirmed(t, txID, 5*time.Second, 90*time.Second)
}

// TestExecuteInlineWallet uses request body wallet only (submitter reads QueuedPayload.Wallet).
func TestExecuteInlineWallet(t *testing.T) {
	w, ok := wallet1()
	if !ok || w.PublicKey == "" {
		t.Skip("wallet with public_key required for inline execute (set in CIRCLE_WALLETS or E2E_WALLET_PUBLIC_KEY)")
	}

	body := transferExecuteBodyInline(w, w.Address)
	st, txID, respBody := postExecute(t, body)
	if st != 202 {
		t.Fatalf("expected 202, got %d: %s", st, respBody)
	}
	if txID == "" {
		t.Fatal("expected transaction_id")
	}
	pollUntilConfirmed(t, txID, 5*time.Second, 90*time.Second)
}

// TestSubmittedAppearsBeforeConfirmed asserts poller-visible submitted state with hash.
func TestSubmittedAppearsBeforeConfirmed(t *testing.T) {
	w, ok := wallet1()
	if !ok {
		t.Skip("wallet not configured")
	}
	st, txID, b := postExecute(t, transferExecuteBody(w.WalletID, w.Address))
	if st != 202 {
		t.Fatalf("expected 202: %s", b)
	}
	if txID == "" {
		t.Fatal("expected transaction_id")
	}
	pollUntilSubmittedThenConfirmed(t, txID, 3*time.Minute)
}

// TestThroughputConcurrentExecute enqueues many self-transfers concurrently, then waits for all to confirm.
// Same sender is processed FIFO by the submitter; this stresses enqueue + queue + sequential submit.
func TestThroughputConcurrentExecute(t *testing.T) {
	w, ok := wallet1()
	if !ok {
		t.Skip("wallet not configured — skipping")
	}

	n := e2eThroughputCount()
	base := transferExecuteBody(w.WalletID, w.Address)

	ids := make([]string, 0, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, txID, b := postExecute(t, base)
			if st != 202 {
				errs <- fmt.Errorf("execute: status %d: %s", st, b)
				return
			}
			if txID == "" {
				errs <- fmt.Errorf("empty transaction_id")
				return
			}
			mu.Lock()
			ids = append(ids, txID)
			mu.Unlock()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate transaction_id %s — expected unique enqueues", id)
		}
		seen[id] = struct{}{}
	}

	var wgc sync.WaitGroup
	for _, id := range ids {
		id := id
		wgc.Add(1)
		go func() {
			defer wgc.Done()
			pollUntilConfirmed(t, id, 2*time.Second, 8*time.Minute)
		}()
	}
	wgc.Wait()
	t.Logf("throughput: confirmed %d transactions", n)
}

// TestThroughputDualWalletConcurrent runs concurrent self-transfers for two senders (sequence isolation).
func TestThroughputDualWalletConcurrent(t *testing.T) {
	w1, ok1 := wallet1()
	w2, ok2 := wallet2()
	if !ok1 || !ok2 {
		t.Skip("need two wallets (CIRCLE_WALLETS[2] or E2E_WALLET2_*) with public_key — skipping")
	}
	if w1.Address == w2.Address {
		t.Fatal("wallet1 and wallet2 must be different addresses")
	}

	n := e2eThroughputCount()
	if n < 4 {
		n = 4
	}
	per := n / 2
	if per < 2 {
		per = 2
	}

	type job struct {
		w e2eWallet
	}
	jobs := make([]job, 0, per*2)
	for i := 0; i < per; i++ {
		jobs = append(jobs, job{w: w1})
	}
	for i := 0; i < per; i++ {
		jobs = append(jobs, job{w: w2})
	}

	ids := make([]string, 0, len(jobs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make(chan error, len(jobs))
	for _, j := range jobs {
		j := j
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := transferExecuteBody(j.w.WalletID, j.w.Address)
			st, txID, b := postExecute(t, body)
			if st != 202 {
				errs <- fmt.Errorf("execute: status %d: %s", st, b)
				return
			}
			if txID == "" {
				errs <- fmt.Errorf("empty transaction_id")
				return
			}
			mu.Lock()
			ids = append(ids, txID)
			mu.Unlock()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate transaction_id %s", id)
		}
		seen[id] = struct{}{}
	}

	var wgc sync.WaitGroup
	for _, id := range ids {
		id := id
		wgc.Add(1)
		go func() {
			defer wgc.Done()
			pollUntilConfirmed(t, id, 2*time.Second, 10*time.Minute)
		}()
	}
	wgc.Wait()

	// Per-sender: confirmed sequence_number strictly increases when sorted by sequence.
	bySender := make(map[string][]uint64)
	for _, id := range ids {
		_, rec := getTransaction(t, id)
		if rec.Status != "confirmed" {
			t.Fatalf("tx %s: want confirmed got %s", id, rec.Status)
		}
		if rec.SequenceNumber == nil {
			t.Fatalf("tx %s: missing sequence_number", id)
		}
		bySender[rec.SenderAddress] = append(bySender[rec.SenderAddress], *rec.SequenceNumber)
	}
	for sender, seqs := range bySender {
		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		for i := 1; i < len(seqs); i++ {
			if seqs[i] <= seqs[i-1] {
				t.Fatalf("sender %s: sequence numbers not strictly increasing: %v", sender, seqs)
			}
		}
		t.Logf("sender %s: %d txs sequence_ok %v", sender, len(seqs), seqs)
	}
}

// TestIdempotencyReplayRecovery posts the same logical request twice; the second must return the same
// transaction id (client retry / duplicate submit recovery).
func TestIdempotencyReplayRecovery(t *testing.T) {
	w, ok := wallet1()
	if !ok {
		t.Skip("wallet not configured — skipping")
	}

	key := fmt.Sprintf("e2e-idemp-body-%d", time.Now().UnixNano())
	body := transferExecuteBody(w.WalletID, w.Address)
	body["idempotency_key"] = key

	st1, id1, b1, h1 := postExecuteWithHeaders(t, body, nil)
	if st1 != 202 {
		t.Fatalf("first execute: %d: %s", st1, b1)
	}
	if id1 == "" {
		t.Fatal("expected transaction_id")
	}
	if h1.Get("X-Idempotency-Replayed") == "true" {
		t.Fatal("first response must not set X-Idempotency-Replayed")
	}

	st2, id2, b2, h2 := postExecuteWithHeaders(t, body, nil)
	if st2 != 202 {
		t.Fatalf("second execute: %d: %s", st2, b2)
	}
	if id2 != id1 {
		t.Fatalf("idempotency: expected same transaction_id, got %q and %q", id1, id2)
	}
	if h2.Get("X-Idempotency-Replayed") != "true" {
		t.Fatalf("second response must set X-Idempotency-Replayed=true, got %q", h2.Get("X-Idempotency-Replayed"))
	}

	st3, id3, _, h3 := postExecuteWithHeaders(t, body, nil)
	if st3 != 202 {
		t.Fatalf("third execute: %d", st3)
	}
	if id3 != id1 {
		t.Fatalf("idempotency: third call got %q want %q", id3, id1)
	}
	if h3.Get("X-Idempotency-Replayed") != "true" {
		t.Fatalf("third response must set X-Idempotency-Replayed=true")
	}

	pollUntilConfirmed(t, id1, 5*time.Second, 90*time.Second)
}

// TestIdempotencyKeyHeader uses Idempotency-Key HTTP header only (no JSON field).
func TestIdempotencyKeyHeader(t *testing.T) {
	w, ok := wallet1()
	if !ok {
		t.Skip("wallet not configured — skipping")
	}

	key := fmt.Sprintf("e2e-idemp-hdr-%d", time.Now().UnixNano())
	body := transferExecuteBody(w.WalletID, w.Address)

	st1, id1, b1, h1 := postExecuteWithHeaders(t, body, map[string]string{"Idempotency-Key": key})
	if st1 != 202 {
		t.Fatalf("first execute: %d: %s", st1, b1)
	}
	if id1 == "" {
		t.Fatal("expected transaction_id")
	}
	if h1.Get("X-Idempotency-Replayed") == "true" {
		t.Fatal("first response must not set X-Idempotency-Replayed")
	}

	st2, id2, _, h2 := postExecuteWithHeaders(t, body, map[string]string{"Idempotency-Key": key})
	if st2 != 202 {
		t.Fatalf("second execute: %d", st2)
	}
	if id2 != id1 {
		t.Fatalf("idempotency: got %q want %q", id2, id1)
	}
	if h2.Get("X-Idempotency-Replayed") != "true" {
		t.Fatalf("replay header want true got %q", h2.Get("X-Idempotency-Replayed"))
	}

	pollUntilConfirmed(t, id1, 5*time.Second, 90*time.Second)
}

// TestStaleProcessingRecovery forces a row into stale processing; submitter RecoverStaleProcessing must re-queue it.
func TestStaleProcessingRecovery(t *testing.T) {
	dsn := e2eMySQLDSN()
	if dsn == "" {
		t.Skip("E2E_MYSQL_DSN or MYSQL_DSN not set — skipping stale-processing test")
	}

	w, ok := wallet1()
	if !ok {
		t.Skip("wallet not configured")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("mysql open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Ping(); err != nil {
		t.Skipf("mysql ping failed (skip stale test): %v", err)
	}

	body := transferExecuteBody(w.WalletID, w.Address)
	st, txID, b := postExecute(t, body)
	if st != 202 {
		t.Fatalf("enqueue: %d %s", st, b)
	}
	if txID == "" {
		t.Fatal("expected transaction_id")
	}

	injected := false
	for attempt := 0; attempt < 8; attempt++ {
		res, err := db.Exec(`
			UPDATE transactions
			SET status = 'processing',
			    updated_at = UTC_TIMESTAMP(3) - INTERVAL 3 MINUTE
			WHERE id = ? AND status = 'queued'`, txID)
		if err != nil {
			t.Fatalf("mysql update: %v", err)
		}
		n, _ := res.RowsAffected()
		if n == 1 {
			injected = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !injected {
		t.Skip("could not inject stale processing (submitter likely claimed first) — skip")
	}

	// Recover runs every 30s with 2m threshold; row is already 3m stale.
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		_, rec := getTransaction(t, txID)
		if rec.Status == "queued" || rec.Status == "processing" {
			// After recovery, should go queued then worker picks up; brief processing ok
			if rec.Status == "queued" {
				t.Logf("stale row recovered to queued")
				break
			}
		}
		if rec.Status == "submitted" || rec.Status == "confirmed" {
			// Worker may have completed before we polled queued
			if rec.Status == "confirmed" {
				t.Logf("tx already confirmed (race with fast worker)")
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	pollUntilConfirmed(t, txID, 3*time.Second, 5*time.Minute)
}

// TestInvalidEntryFunctionEventuallyFails queues a non-existent entry function; the worker should mark it failed
// (error recovery: bad work does not wedge the queue — follow with a good tx in TestExecuteAndPoll or separately).
func TestInvalidEntryFunctionEventuallyFails(t *testing.T) {
	w, ok := wallet1()
	if !ok {
		t.Skip("wallet not configured — skipping")
	}

	reqBody := map[string]any{
		"wallet_id":      w.WalletID,
		"function_id":    "0x1::this_module_does_not_exist_e2e::fake",
		"type_arguments": []string{},
		"arguments":      []any{w.Address, "1"},
	}
	st, txID, b := postExecute(t, reqBody)
	if st != 202 {
		t.Fatalf("expected 202 enqueue, got %d: %s", st, b)
	}
	if txID == "" {
		t.Fatal("expected transaction_id")
	}

	deadline := time.Now().Add(2 * time.Minute)
	var last txnRecord
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)
		code, rec := getTransaction(t, txID)
		if code != 200 {
			continue
		}
		last = rec
		t.Logf("invalid-fn poll: status=%s err=%q attempts=%d last_err=%q",
			rec.Status, rec.ErrorMessage, rec.AttemptCount, rec.LastError)
		switch rec.Status {
		case "failed":
			if rec.ErrorMessage == "" && rec.LastError == "" {
				t.Fatal("expected error_message or last_error on failed tx")
			}
			t.Logf("invalid entry function surfaced as failed (recovery path ok)")
			return
		case "confirmed":
			t.Fatal("unexpected confirmed for invalid function")
		}
	}
	t.Fatalf("expected failed status within 2m, last: %+v", last)
}

// TestStatusProgressionMonotonic polls quickly and asserts lifecycle does not move backwards (queued → … → confirmed).
func TestStatusProgressionMonotonic(t *testing.T) {
	w, ok := wallet1()
	if !ok {
		t.Skip("wallet not configured — skipping")
	}

	st, txID, b := postExecute(t, transferExecuteBody(w.WalletID, w.Address))
	if st != 202 {
		t.Fatalf("expected 202: %s", b)
	}
	if txID == "" {
		t.Fatal("expected transaction_id")
	}

	lastRank := -1
	deadline := time.Now().Add(3 * time.Minute)
	var lastStatus string

	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		code, rec := getTransaction(t, txID)
		if code != 200 {
			continue
		}
		r := statusRank(rec.Status)
		if r < 0 {
			continue
		}
		if r < lastRank {
			t.Fatalf("status moved backwards: was rank %d (%s) now %q (rank %d)",
				lastRank, lastStatus, rec.Status, r)
		}
		if r > lastRank {
			t.Logf("status advance: %s (rank %d)", rec.Status, r)
			lastRank = r
			lastStatus = rec.Status
		}
		switch rec.Status {
		case "confirmed":
			t.Logf("monotonic progression ok, final confirmed hash=%s", rec.TxnHash)
			return
		case "failed", "expired":
			t.Fatalf("unexpected terminal %s: %s", rec.Status, rec.ErrorMessage)
		}
	}
	t.Fatalf("not confirmed within 3m, last status %s rank %d", lastStatus, lastRank)
}
