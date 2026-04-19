package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
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
	BaseURL string
	APIKey  string

	Alice   wallet // power user — lots of executes
	Bob     wallet // mixed — execute + query
	Charlie string // read-only — just an address to watch via query

	// How aggressive each persona is. Tune via env to exaggerate the
	// per-sender parallelism in the timing output.
	AliceTxns  int
	BobTxns    int
	QueryEvery time.Duration
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

	alice := wallet{WalletID: os.Getenv("ALICE_WALLET_ID"), Address: os.Getenv("ALICE_ADDRESS")}
	bob := wallet{WalletID: os.Getenv("BOB_WALLET_ID"), Address: os.Getenv("BOB_ADDRESS")}
	if alice.WalletID == "" || alice.Address == "" {
		log.Fatal("ALICE_WALLET_ID and ALICE_ADDRESS are required")
	}
	if bob.WalletID == "" || bob.Address == "" {
		log.Fatal("BOB_WALLET_ID and BOB_ADDRESS are required")
	}
	charlie := os.Getenv("CHARLIE_WATCH_ADDRESS")
	if charlie == "" {
		charlie = alice.Address
	}

	return config{
		BaseURL:    base,
		APIKey:     apiKey,
		Alice:      alice,
		Bob:        bob,
		Charlie:    charlie,
		AliceTxns:  10,
		BobTxns:    3,
		QueryEvery: 1 * time.Second,
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

// logf prefixes every line with an elapsed-time stamp so you can eyeball
// concurrency by looking at when each persona's actions interleave.
func logf(start time.Time, persona, format string, args ...any) {
	elapsed := time.Since(start).Round(time.Millisecond)
	log.Printf("[%6s %s] "+format, append([]any{elapsed, persona}, args...)...)
}

// ---------------------------------------------------------------------------
// Personas
// ---------------------------------------------------------------------------

// runAlice submits a stream of self-transfers via /v1/execute.
//
// Because the service enforces per-sender FIFO order, all of Alice's
// transactions will be assigned sequence numbers in the order they were
// received by the API — even though we're submitting concurrently.
func runAlice(ctx context.Context, cfg config, start time.Time) []string {
	var wg sync.WaitGroup
	ids := make([]string, cfg.AliceTxns)

	for i := range cfg.AliceTxns {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			body := map[string]any{
				"wallet_id":       cfg.Alice.WalletID,
				"address":         cfg.Alice.Address,
				"function_id":     "0x1::aptos_account::transfer",
				"arguments":       []any{cfg.Alice.Address, "1"},
				"idempotency_key": uuid.New().String(),
			}
			code, resp, err := doPost(ctx, cfg.BaseURL+"/v1/execute", cfg.APIKey, body)
			if err != nil || code != http.StatusAccepted {
				logf(start, "Alice", "submit #%d failed: code=%d err=%v resp=%s", i, code, err, resp)
				return
			}
			var r struct {
				TransactionID string `json:"transaction_id"`
			}
			_ = json.Unmarshal(resp, &r)
			ids[i] = r.TransactionID
			logf(start, "Alice", "queued #%d -> %s", i, r.TransactionID)
		}(i)
	}
	wg.Wait()
	logf(start, "Alice", "finished submitting %d transfers", cfg.AliceTxns)
	return ids
}

// runBob alternates between occasional transfers (/v1/execute) and frequent
// balance queries (/v1/query). Bob demonstrates the more realistic backend
// pattern: "do a thing, then check state".
func runBob(ctx context.Context, cfg config, start time.Time) []string {
	ids := make([]string, 0, cfg.BobTxns)

	for i := range cfg.BobTxns {
		// Query balance first.
		balance, err := queryBalance(ctx, cfg, cfg.Bob.Address)
		if err != nil {
			logf(start, "Bob", "balance query failed: %v", err)
		} else {
			logf(start, "Bob", "balance=%s", balance)
		}

		// Submit a transfer.
		body := map[string]any{
			"wallet_id":       cfg.Bob.WalletID,
			"address":         cfg.Bob.Address,
			"function_id":     "0x1::aptos_account::transfer",
			"arguments":       []any{cfg.Bob.Address, "1"},
			"idempotency_key": uuid.New().String(),
		}
		code, resp, err := doPost(ctx, cfg.BaseURL+"/v1/execute", cfg.APIKey, body)
		if err != nil || code != http.StatusAccepted {
			logf(start, "Bob", "submit #%d failed: code=%d err=%v resp=%s", i, code, err, resp)
			continue
		}
		var r struct {
			TransactionID string `json:"transaction_id"`
		}
		_ = json.Unmarshal(resp, &r)
		ids = append(ids, r.TransactionID)
		logf(start, "Bob", "queued #%d -> %s", i, r.TransactionID)

		// Breathe between actions so the logs interleave visibly with Alice.
		time.Sleep(400 * time.Millisecond)
	}
	logf(start, "Bob", "finished workload")
	return ids
}

// runCharlie is read-only. He loops querying balances and watching a
// transaction — if the server queues work, Charlie should NOT notice any
// slowdown because /v1/query is synchronous and doesn't touch the submitter.
func runCharlie(ctx context.Context, cfg config, watchTxID <-chan string, start time.Time) {
	ticker := time.NewTicker(cfg.QueryEvery)
	defer ticker.Stop()

	var watching string

	for {
		select {
		case <-ctx.Done():
			logf(start, "Charlie", "shutting down")
			return
		case id, ok := <-watchTxID:
			if !ok {
				watchTxID = nil
				continue
			}
			watching = id
			logf(start, "Charlie", "now watching %s", id)
		case <-ticker.C:
			// Query a random watched address's balance.
			balance, err := queryBalance(ctx, cfg, cfg.Charlie)
			if err != nil {
				logf(start, "Charlie", "balance query failed: %v", err)
			} else {
				logf(start, "Charlie", "watched-balance=%s", balance)
			}

			// Poll the watched transaction if we have one.
			if watching != "" {
				status, err := queryTxStatus(ctx, cfg, watching)
				if err != nil {
					logf(start, "Charlie", "tx-status query failed: %v", err)
				} else {
					logf(start, "Charlie", "tx %s status=%s", shortID(watching), status)
					if status == "confirmed" || status == "failed" {
						watching = "" // stop following this one
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// /v1/query and /v1/transactions/{id} helpers
// ---------------------------------------------------------------------------

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
		return "", fmt.Errorf("query: code=%d body=%s", code, resp)
	}
	var r struct {
		Result any `json:"result"`
	}
	_ = json.Unmarshal(resp, &r)
	return fmt.Sprintf("%v", r.Result), nil
}

func queryTxStatus(ctx context.Context, cfg config, txID string) (string, error) {
	code, resp, err := doGet(ctx, cfg.BaseURL+"/v1/transactions/"+txID, cfg.APIKey)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("status: code=%d body=%s", code, resp)
	}
	var r struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(resp, &r)
	return r.Status, nil
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ---------------------------------------------------------------------------
// Confirmation waiter (reuses queryTxStatus)
// ---------------------------------------------------------------------------

func waitForAll(ctx context.Context, cfg config, ids []string, timeout time.Duration, start time.Time) {
	deadline := time.Now().Add(timeout)
	var done atomic.Int64
	var wg sync.WaitGroup

	for _, id := range ids {
		if id == "" {
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			backoff := 500 * time.Millisecond
			for time.Now().Before(deadline) {
				status, err := queryTxStatus(ctx, cfg, id)
				if err == nil && (status == "confirmed" || status == "failed") {
					done.Add(1)
					logf(start, "main", "tx %s => %s", shortID(id), status)
					return
				}
				jitter := time.Duration(rand.Int64N(int64(backoff / 4)))
				time.Sleep(backoff + jitter)
				backoff = min(backoff*2, 5*time.Second)
			}
		}(id)
	}
	wg.Wait()
	logf(start, "main", "%d / %d txns reached a terminal status", done.Load(), len(ids))
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	cfg := loadConfig()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Printf("Circle Wallet Service — multi-user example")
	log.Printf("base_url=%s alice=%s bob=%s charlie=%s",
		cfg.BaseURL, shortID(cfg.Alice.Address), shortID(cfg.Bob.Address), shortID(cfg.Charlie))

	start := time.Now()

	// Charlie runs until the shared ctx is cancelled. We feed him one of Alice's
	// transaction IDs as something to watch, to show /v1/transactions/{id} in
	// action alongside /v1/query.
	watchCh := make(chan string, 1)
	charlieDone := make(chan struct{})
	go func() {
		defer close(charlieDone)
		runCharlie(ctx, cfg, watchCh, start)
	}()

	// Alice and Bob run concurrently — different senders, so the submitter
	// spawns a worker goroutine for each and their executes proceed in parallel.
	var wg sync.WaitGroup
	var aliceIDs, bobIDs []string

	wg.Add(2)
	go func() {
		defer wg.Done()
		aliceIDs = runAlice(ctx, cfg, start)
		// Hand one of Alice's IDs to Charlie to watch.
		for _, id := range aliceIDs {
			if id != "" {
				select {
				case watchCh <- id:
				default:
				}
				break
			}
		}
	}()
	go func() {
		defer wg.Done()
		bobIDs = runBob(ctx, cfg, start)
	}()

	wg.Wait()
	close(watchCh)

	// Wait for all submitted txns to settle. In a well-configured system every
	// one of them should confirm within a few seconds.
	all := append(append([]string{}, aliceIDs...), bobIDs...)
	waitForAll(ctx, cfg, all, 90*time.Second, start)

	cancel() // stop Charlie
	<-charlieDone

	logf(start, "main", "example complete")
}
