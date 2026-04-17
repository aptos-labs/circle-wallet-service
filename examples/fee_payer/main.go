package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type wallet struct {
	WalletID string
	Address  string
}

type config struct {
	BaseURL   string
	APIKey    string
	User      wallet
	Sponsor   wallet
	Recipient string
}

func loadConfig() config {
	_ = godotenv.Load()

	base := os.Getenv("API_BASE_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	apiKey := os.Getenv("API_KEY")
	if apiKey == "" {
		log.Fatal("API_KEY is required")
	}

	user := wallet{WalletID: os.Getenv("USER_WALLET_ID"), Address: os.Getenv("USER_ADDRESS")}
	sponsor := wallet{WalletID: os.Getenv("SPONSOR_WALLET_ID"), Address: os.Getenv("SPONSOR_ADDRESS")}
	if user.WalletID == "" || user.Address == "" {
		log.Fatal("USER_WALLET_ID and USER_ADDRESS are required")
	}
	if sponsor.WalletID == "" || sponsor.Address == "" {
		log.Fatal("SPONSOR_WALLET_ID and SPONSOR_ADDRESS are required")
	}

	recipient := os.Getenv("RECIPIENT_ADDRESS")
	if recipient == "" {
		recipient = sponsor.Address
	}

	return config{
		BaseURL:   base,
		APIKey:    apiKey,
		User:      user,
		Sponsor:   sponsor,
		Recipient: recipient,
	}
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

var httpClient = &http.Client{Timeout: 30 * time.Second}

func doPost(ctx context.Context, url, apiKey string, body any) (int, []byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}

func doGet(ctx context.Context, url, apiKey string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}

// ---------------------------------------------------------------------------
// Balance query
// ---------------------------------------------------------------------------

// queryBalance reads the APT balance of addr via the view function
// 0x1::coin::balance<0x1::aptos_coin::AptosCoin>. Returns the raw octas (the
// smallest unit of APT, 10^8 per 1 APT) as a string because JSON numbers lose
// precision above 2^53.
func queryBalance(ctx context.Context, cfg config, addr string) (string, error) {
	body := map[string]any{
		"function_id":    "0x1::coin::balance",
		"type_arguments": []string{"0x1::aptos_coin::AptosCoin"},
		"arguments":      []any{addr},
	}
	code, resp, err := doPost(ctx, cfg.BaseURL+"/v1/query", cfg.APIKey, body)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("query balance: code=%d body=%s", code, resp)
	}
	var r struct {
		Result any `json:"result"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return "", err
	}
	return fmt.Sprintf("%v", r.Result), nil
}

// ---------------------------------------------------------------------------
// Execute + poll
// ---------------------------------------------------------------------------

// submitSponsored submits a transfer from user to recipient, with sponsor
// paying gas. Returns the service's opaque transaction ID (not the on-chain
// hash — that shows up later on the poll response as txn_hash).
func submitSponsored(ctx context.Context, cfg config) (string, error) {
	body := map[string]any{
		"wallet_id":   cfg.User.WalletID,
		"address":     cfg.User.Address,
		"function_id": "0x1::aptos_account::transfer",
		"arguments":   []any{cfg.Recipient, "1"},

		// The important bit: name a DIFFERENT Circle wallet as the fee payer.
		// The service will collect two signatures (user's + sponsor's) and
		// build a fee-payer transaction that Aptos accepts.
		"fee_payer": map[string]string{
			"wallet_id": cfg.Sponsor.WalletID,
			"address":   cfg.Sponsor.Address,
		},

		// Idempotency key lets you safely retry this POST if the network
		// cuts out — the service will return the existing record instead
		// of creating a duplicate.
		"idempotency_key": "fee-payer-demo-" + uuid.New().String(),
	}

	code, resp, err := doPost(ctx, cfg.BaseURL+"/v1/execute", cfg.APIKey, body)
	if err != nil {
		return "", err
	}
	if code != http.StatusAccepted {
		return "", fmt.Errorf("execute: code=%d body=%s", code, resp)
	}
	var r struct {
		TransactionID string `json:"transaction_id"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return "", err
	}
	log.Printf("submitted: id=%s status=%s", r.TransactionID, r.Status)
	return r.TransactionID, nil
}

// pollUntilTerminal polls GET /v1/transactions/{id} until the record reaches
// "confirmed" or "failed". Returns the final record as a parsed map so the
// caller can inspect fields like txn_hash, sequence_number, error_message.
func pollUntilTerminal(ctx context.Context, cfg config, txID string, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	backoff := 1 * time.Second

	for time.Now().Before(deadline) {
		code, resp, err := doGet(ctx, cfg.BaseURL+"/v1/transactions/"+txID, cfg.APIKey)
		if err != nil {
			return nil, err
		}
		if code != http.StatusOK {
			return nil, fmt.Errorf("poll: code=%d body=%s", code, resp)
		}

		var rec map[string]any
		if err := json.Unmarshal(resp, &rec); err != nil {
			return nil, err
		}
		status, _ := rec["status"].(string)
		log.Printf("  status=%s hash=%v", status, rec["txn_hash"])

		switch status {
		case "confirmed", "failed":
			return rec, nil
		}

		time.Sleep(backoff)
		backoff = min(backoff*2, 5*time.Second)
	}
	return nil, fmt.Errorf("timed out after %s", timeout)
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	cfg := loadConfig()
	ctx := context.Background()

	log.Println("Circle Wallet Service — fee-payer (sponsored transaction) example")
	log.Printf("user:      wallet_id=%s address=%s", cfg.User.WalletID, cfg.User.Address)
	log.Printf("sponsor:   wallet_id=%s address=%s", cfg.Sponsor.WalletID, cfg.Sponsor.Address)
	log.Printf("recipient: %s", cfg.Recipient)
	log.Println()

	// 1. Read the user's balance before.
	log.Println("--- step 1: query balances before ---")
	userBefore, err := queryBalance(ctx, cfg, cfg.User.Address)
	if err != nil {
		log.Fatalf("query user balance: %v", err)
	}
	sponsorBefore, err := queryBalance(ctx, cfg, cfg.Sponsor.Address)
	if err != nil {
		log.Fatalf("query sponsor balance: %v", err)
	}
	log.Printf("user balance (octas):    %s", userBefore)
	log.Printf("sponsor balance (octas): %s", sponsorBefore)
	log.Println()

	// 2. Submit a sponsored transfer: user → recipient, sponsor pays gas.
	log.Println("--- step 2: submit sponsored transfer ---")
	txID, err := submitSponsored(ctx, cfg)
	if err != nil {
		log.Fatalf("submit: %v", err)
	}
	log.Println()

	// 3. Poll until confirmed.
	log.Println("--- step 3: poll until confirmed ---")
	rec, err := pollUntilTerminal(ctx, cfg, txID, 90*time.Second)
	if err != nil {
		log.Fatalf("poll: %v", err)
	}
	status, _ := rec["status"].(string)
	if status != "confirmed" {
		errMsg, _ := rec["error_message"].(string)
		log.Fatalf("transaction ended in status=%s err=%s", status, errMsg)
	}
	log.Printf("confirmed: hash=%v sequence=%v", rec["txn_hash"], rec["sequence_number"])
	log.Println()

	// 4. Read balances again and summarize.
	log.Println("--- step 4: query balances after ---")
	userAfter, err := queryBalance(ctx, cfg, cfg.User.Address)
	if err != nil {
		log.Fatalf("query user balance: %v", err)
	}
	sponsorAfter, err := queryBalance(ctx, cfg, cfg.Sponsor.Address)
	if err != nil {
		log.Fatalf("query sponsor balance: %v", err)
	}
	log.Printf("user balance (octas):    %s  (was %s)", userAfter, userBefore)
	log.Printf("sponsor balance (octas): %s  (was %s)", sponsorAfter, sponsorBefore)
	log.Println()

	// Interpretation hints for the reader:
	log.Println("interpretation:")
	log.Println("  - user's balance should have dropped by the transfer amount (1 octa)")
	log.Println("    — NOT by gas. If it dropped by more, fee-payer isn't working.")
	log.Println("  - sponsor's balance should have dropped by the gas cost, which is")
	log.Println("    the transaction's gas_used * gas_unit_price (visible on the Aptos")
	log.Println("    explorer at the txn_hash printed above).")
}
