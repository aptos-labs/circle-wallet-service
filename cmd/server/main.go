package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/aptos"
	circle2 "github.com/aptos-labs/jc-contract-integration/internal/circle"
	"github.com/aptos-labs/jc-contract-integration/internal/config"
	handler2 "github.com/aptos-labs/jc-contract-integration/internal/handler"
	"github.com/aptos-labs/jc-contract-integration/internal/poller"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/aptos-labs/jc-contract-integration/internal/webhook"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger.Info("config loaded",
		"port", cfg.ServerPort,
		"aptos_node", cfg.AptosNodeURL,
		"wallets", len(cfg.CircleWallets),
		"testing_mode", cfg.TestingMode,
	)

	// Resolve public keys for wallets that don't have them configured
	circleClient := circle2.NewClient(cfg.CircleAPIKey)
	for i := range cfg.CircleWallets {
		w := &cfg.CircleWallets[i]
		if w.PublicKey != "" {
			continue
		}
		walletResp, err := circleClient.GetWallet(context.Background(), w.WalletID)
		if err != nil {
			return fmt.Errorf("fetch public key for wallet %s: %w", w.WalletID, err)
		}
		pk := walletResp.Data.Wallet.InitialPublicKey
		if pk == "" {
			return fmt.Errorf("wallet %s has no initialPublicKey", w.WalletID)
		}
		if !strings.HasPrefix(pk, "0x") {
			pk = "0x" + pk
		}
		w.PublicKey = pk
		logger.Info("resolved wallet public key", "wallet_id", w.WalletID, "address", w.Address)
	}

	// Initialize components
	aptosClient, err := aptos.NewClient(cfg.AptosNodeURL, cfg.AptosChainID, int64(cfg.TxnExpirationSeconds), cfg.MaxGasAmount)
	if err != nil {
		return fmt.Errorf("init aptos client: %w", err)
	}
	abiCache := aptos.NewABICache(aptosClient.Inner)
	circleSigner := circle2.NewSigner(circleClient, cfg.CircleEntitySecret)
	memStore := store.NewMemoryStore(time.Duration(cfg.StoreTTLSeconds) * time.Second)
	defer func(memStore *store.MemoryStore) {
		err := memStore.Close()
		if err != nil {
			os.Exit(1)
		}
	}(memStore)

	notifier := webhook.NewNotifier(cfg.WebhookURL, logger)
	txnPoller := poller.New(aptosClient, memStore, notifier, time.Duration(cfg.PollIntervalSeconds)*time.Second, logger)

	// Build router
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/execute", handler2.Execute(cfg, aptosClient, abiCache, circleSigner, memStore, logger))
	mux.HandleFunc("POST /v1/query", handler2.Query(aptosClient, abiCache))
	mux.HandleFunc("GET /v1/transactions/{id}", handler2.GetTransaction(memStore))
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"status":"ok"}`))
		if err != nil {
			os.Exit(1)
		}
	})

	// Auth middleware
	var h http.Handler = mux
	if !cfg.TestingMode {
		apiKey := cfg.APIKey
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/health" {
				mux.ServeHTTP(w, r)
				return
			}
			auth := r.Header.Get("Authorization")
			if auth == "" {
				auth = r.Header.Get("X-API-Key")
			}
			if auth != apiKey && auth != "Bearer "+apiKey {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			mux.ServeHTTP(w, r)
		})
	}

	srv := &http.Server{
		Addr:         ":" + cfg.ServerPort,
		Handler:      h,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go txnPoller.Run(ctx)

	go func() {
		logger.Info("server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}
	logger.Info("server stopped")
	return nil
}
