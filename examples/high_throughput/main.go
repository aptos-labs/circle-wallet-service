package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
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
	WalletID string `json:"wallet_id"`
	Address  string `json:"address"`
}

type config struct {
	BaseURL  string
	APIKey   string
	Wallets  []wallet
	TxnCount int
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

	walletsRaw := os.Getenv("WALLETS")
	if walletsRaw == "" {
		log.Fatal("WALLETS is required (JSON array of {wallet_id, address})")
	}
	var wallets []wallet
	if err := json.Unmarshal([]byte(walletsRaw), &wallets); err != nil {
		log.Fatalf("bad WALLETS JSON: %v", err)
	}
	if len(wallets) == 0 {
		log.Fatal("WALLETS must contain at least one wallet")
	}

	n := 20
	if s := os.Getenv("TXN_COUNT"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v <= 0 {
			log.Fatalf("bad TXN_COUNT: %s", s)
		}
		n = v
	}

	return config{BaseURL: base, APIKey: apiKey, Wallets: wallets, TxnCount: n}
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
	defer func() {
		_ = resp.Body.Close()
	}()
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
	defer func() {
		_ = resp.Body.Close()
	}()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}

// ---------------------------------------------------------------------------
// 1. Parallel multi-sender submission
// ---------------------------------------------------------------------------

type submitResult struct {
	TransactionID  string `json:"transaction_id"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"-"`
}

func submitTransactions(ctx context.Context, cfg config) []submitResult {
	sem := make(chan struct{}, 50) // limit concurrent HTTP calls
	var mu sync.Mutex
	results := make([]submitResult, 0, cfg.TxnCount)
	var wg sync.WaitGroup
	var submitted atomic.Int64

	start := time.Now()

	for i := range cfg.TxnCount {
		wg.Add(1)
		w := cfg.Wallets[i%len(cfg.Wallets)] // round-robin across wallets
		idempKey := uuid.New().String()

		go func(idx int, w wallet, idempKey string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			body := map[string]any{
				"wallet_id":       w.WalletID,
				"address":         w.Address,
				"function_id":     "0x1::aptos_account::transfer",
				"arguments":       []any{w.Address, "1"},
				"idempotency_key": idempKey,
			}

			code, resp, err := doPost(ctx, cfg.BaseURL+"/v1/execute", cfg.APIKey, body)
			if err != nil {
				log.Printf("[submit %d] error: %v", idx, err)
				return
			}
			if code == http.StatusTooManyRequests {
				log.Printf("[submit %d] 429 — back off and retry", idx)
				return
			}
			if code != http.StatusAccepted {
				log.Printf("[submit %d] unexpected %d: %s", idx, code, resp)
				return
			}

			var r submitResult
			_ = json.Unmarshal(resp, &r)
			r.IdempotencyKey = idempKey

			mu.Lock()
			results = append(results, r)
			mu.Unlock()

			n := submitted.Add(1)
			if n%50 == 0 || n == int64(cfg.TxnCount) {
				log.Printf("submitted %d / %d", n, cfg.TxnCount)
			}
		}(i, w, idempKey)
	}

	wg.Wait()
	elapsed := time.Since(start)
	log.Printf("submission complete: %d txns in %s (%.1f txn/s)",
		len(results), elapsed.Round(time.Millisecond), float64(len(results))/elapsed.Seconds())

	return results
}

// ---------------------------------------------------------------------------
// 2. Concurrent polling with exponential backoff
// ---------------------------------------------------------------------------

func pollTransactions(ctx context.Context, cfg config, txns []submitResult) {
	start := time.Now()
	var confirmed, failed atomic.Int64
	var wg sync.WaitGroup

	sem := make(chan struct{}, 50)

	for _, tx := range txns {
		wg.Add(1)
		go func(txID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			backoff := 200 * time.Millisecond
			const maxBackoff = 5 * time.Second

			for {
				code, body, err := doGet(ctx, cfg.BaseURL+"/v1/transactions/"+txID, cfg.APIKey)
				if err != nil {
					log.Printf("[poll %s] error: %v", txID[:8], err)
					time.Sleep(backoff)
					backoff = min(backoff*2, maxBackoff)
					continue
				}
				if code == http.StatusTooManyRequests {
					time.Sleep(backoff)
					backoff = min(backoff*2, maxBackoff)
					continue
				}

				var rec struct {
					Status string `json:"status"`
				}
				_ = json.Unmarshal(body, &rec)

				switch rec.Status {
				case "confirmed":
					confirmed.Add(1)
					return
				case "failed", "expired":
					failed.Add(1)
					log.Printf("[poll %s] terminal status: %s", txID[:8], rec.Status)
					return
				default:
					jitter := time.Duration(rand.Int64N(int64(backoff / 4)))
					time.Sleep(backoff + jitter)
					backoff = min(backoff*2, maxBackoff)
				}
			}
		}(tx.TransactionID)
	}

	wg.Wait()
	elapsed := time.Since(start)
	log.Printf("polling complete: %d confirmed, %d failed/expired in %s",
		confirmed.Load(), failed.Load(), elapsed.Round(time.Millisecond))
}

// ---------------------------------------------------------------------------
// 3. Idempotent retry demonstration
// ---------------------------------------------------------------------------

func demonstrateIdempotency(ctx context.Context, cfg config) {
	w := cfg.Wallets[0]
	idempKey := "demo-idemp-" + uuid.New().String()

	body := map[string]any{
		"wallet_id":       w.WalletID,
		"address":         w.Address,
		"function_id":     "0x1::aptos_account::transfer",
		"arguments":       []any{w.Address, "1"},
		"idempotency_key": idempKey,
	}

	log.Println("--- idempotency demo: submitting same key twice ---")

	code1, resp1, _ := doPost(ctx, cfg.BaseURL+"/v1/execute", cfg.APIKey, body)
	log.Printf("  1st call: %d %s", code1, resp1)

	code2, resp2, _ := doPost(ctx, cfg.BaseURL+"/v1/execute", cfg.APIKey, body)
	log.Printf("  2nd call: %d %s (should return same transaction_id)", code2, resp2)
}

// ---------------------------------------------------------------------------
// 4. Webhook listener sketch
// ---------------------------------------------------------------------------

func startWebhookListener(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		log.Printf("[webhook] received: %s", body)
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("webhook listener on %s/webhook", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Printf("webhook listener error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Throughput summary
// ---------------------------------------------------------------------------

func printSummary(cfg config, totalStart time.Time, count int) {
	elapsed := time.Since(totalStart)
	log.Println("=========================================")
	log.Printf("wallets:        %d", len(cfg.Wallets))
	log.Printf("transactions:   %d", count)
	log.Printf("total time:     %s", elapsed.Round(time.Millisecond))
	if elapsed.Seconds() > 0 {
		log.Printf("throughput:     %.1f txn/s (submit → confirm)", float64(count)/elapsed.Seconds())
	}
	log.Printf("expected scale: ~%.0f× throughput with %d wallets",
		math.Min(float64(len(cfg.Wallets)), float64(count)), len(cfg.Wallets))
	log.Println("=========================================")
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	cfg := loadConfig()
	ctx := context.Background()

	log.Printf("Circle Wallet Service — high-throughput example")
	log.Printf("base_url=%s  wallets=%d  txn_count=%d", cfg.BaseURL, len(cfg.Wallets), cfg.TxnCount)

	// Optional: start a local webhook listener. When using webhooks, pass
	// "webhook_url": "http://your-host:9090/webhook" in the execute request
	// to skip polling entirely.
	webhookCtx, webhookCancel := context.WithCancel(ctx)
	defer webhookCancel()
	go startWebhookListener(webhookCtx, ":9090")

	// Idempotency demo
	demonstrateIdempotency(ctx, cfg)

	// Submit
	totalStart := time.Now()
	txns := submitTransactions(ctx, cfg)
	if len(txns) == 0 {
		log.Fatal("no transactions submitted")
	}

	// Poll
	pollTransactions(ctx, cfg, txns)

	// Summary
	printSummary(cfg, totalStart, len(txns))

	webhookCancel()
	time.Sleep(100 * time.Millisecond) // let webhook server drain
}
