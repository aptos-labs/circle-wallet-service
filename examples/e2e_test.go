// Package examples provides end-to-end tests against a live server.
//
// Usage:
//
//  1. Copy rewrite/.env.example to rewrite/.env and fill in your credentials
//  2. Start the server:  cd rewrite && go run ./cmd/server
//  3. Run these tests:   cd rewrite && go test ./examples/ -v -count=1
//
// Environment variables (override via shell or .env):
//
//	E2E_BASE_URL   — server URL (default http://localhost:8080)
//	E2E_API_KEY    — must match the server's API_KEY
//	E2E_WALLET_ID  — Circle wallet ID to use for execute
//	E2E_WALLET_ADDR — Aptos address of that wallet
package examples

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

func init() {
	// Best-effort load .env from rewrite/ directory
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

func walletID() string {
	if v := os.Getenv("E2E_WALLET_ID"); v != "" {
		return v
	}
	// Try to parse from CIRCLE_WALLETS env
	raw := os.Getenv("CIRCLE_WALLETS")
	if raw == "" {
		return ""
	}
	var wallets []struct {
		WalletID string `json:"wallet_id"`
	}
	if err := json.Unmarshal([]byte(raw), &wallets); err == nil && len(wallets) > 0 {
		return wallets[0].WalletID
	}
	return ""
}

func walletAddr() string {
	if v := os.Getenv("E2E_WALLET_ADDR"); v != "" {
		return v
	}
	raw := os.Getenv("CIRCLE_WALLETS")
	if raw == "" {
		return ""
	}
	var wallets []struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal([]byte(raw), &wallets); err == nil && len(wallets) > 0 {
		return wallets[0].Address
	}
	return ""
}

// doRequest is a helper that makes an HTTP request and returns status code + body.
func doRequest(t *testing.T, method, url string, body any, headers map[string]string) (int, []byte) {
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
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, respBody
}

func authHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + apiKey()}
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

func TestExecuteAndPoll(t *testing.T) {
	wid := walletID()
	addr := walletAddr()
	if wid == "" || addr == "" {
		t.Skip("E2E_WALLET_ID/E2E_WALLET_ADDR or CIRCLE_WALLETS not set — skipping execute test")
	}

	// Submit: transfer 1 octa to self (harmless)
	reqBody := map[string]any{
		"wallet_id":      wid,
		"function_id":    "0x1::aptos_account::transfer",
		"type_arguments": []string{},
		"arguments":      []any{addr, "1"},
	}

	status, body := doRequest(t, "POST", baseURL()+"/v1/execute", reqBody, authHeaders())
	t.Logf("Execute response (%d): %s", status, body)

	if status != 202 {
		t.Fatalf("expected 202, got %d: %s", status, body)
	}

	var execResp struct {
		TransactionID string `json:"transaction_id"`
		Status        string `json:"status"`
		TxnHash       string `json:"txn_hash"`
	}
	if err := json.Unmarshal(body, &execResp); err != nil {
		t.Fatalf("parse execute response: %v", err)
	}
	if execResp.TransactionID == "" {
		t.Fatal("expected transaction_id in response")
	}
	if execResp.TxnHash == "" {
		t.Fatal("expected txn_hash in response")
	}
	t.Logf("Transaction ID: %s", execResp.TransactionID)
	t.Logf("Transaction Hash: %s", execResp.TxnHash)

	// Poll for confirmation (up to 60s)
	pollURL := fmt.Sprintf("%s/v1/transactions/%s", baseURL(), execResp.TransactionID)
	var finalStatus string

	for i := range 12 {
		time.Sleep(5 * time.Second)

		pollStatus, pollBody := doRequest(t, "GET", pollURL, nil, authHeaders())
		if pollStatus != 200 {
			t.Logf("Poll %d: HTTP %d", i+1, pollStatus)
			continue
		}

		var txnResp struct {
			Status       string `json:"status"`
			ErrorMessage string `json:"error_message"`
		}
		if err := json.Unmarshal(pollBody, &txnResp); err != nil {
			t.Logf("Poll %d: parse error: %v", i+1, err)
			continue
		}

		t.Logf("Poll %d: status=%s", i+1, txnResp.Status)
		finalStatus = txnResp.Status

		switch txnResp.Status {
		case "confirmed":
			t.Logf("Transaction confirmed!")
			return
		case "failed":
			t.Fatalf("Transaction failed: %s", txnResp.ErrorMessage)
		case "expired":
			t.Fatalf("Transaction expired: %s", txnResp.ErrorMessage)
		}
	}

	t.Fatalf("Transaction not confirmed after 60s, last status: %s", finalStatus)
}
