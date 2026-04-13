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
	"github.com/aptos-labs/jc-contract-integration/internal/idempotency"
	"github.com/aptos-labs/jc-contract-integration/internal/nonce"
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
	var circleClient *circle2.Client
	var circleSigner *circle2.Signer
	if cfg.CircleAPIKey != "" && cfg.CircleEntitySecret != "" {
		circleClient = circle2.NewClient(cfg.CircleAPIKey)
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
		circleSigner = circle2.NewSigner(circleClient, cfg.CircleEntitySecret)
	} else {
		logger.Warn("Circle credentials not fully configured; execute endpoint will not work")
	}

	// Initialize components
	var aptosClient *aptos.Client
	var abiCache *aptos.ABICache
	if cfg.AptosNodeURL != "" {
		aptosClient, err = aptos.NewClient(cfg.AptosNodeURL, cfg.AptosChainID, int64(cfg.TxnExpirationSeconds), cfg.MaxGasAmount)
		if err != nil {
			return fmt.Errorf("init aptos client: %w", err)
		}
		abiCache = aptos.NewABICache(aptosClient.Inner)
	} else {
		logger.Warn("APTOS_NODE_URL not configured; execute and query endpoints will not work")
	}
	memStore := store.NewMemoryStore(time.Duration(cfg.StoreTTLSeconds) * time.Second)
	defer func(memStore *store.MemoryStore) {
		err := memStore.Close()
		if err != nil {
			os.Exit(1)
		}
	}(memStore)

	nonceStore := nonce.NewStore(time.Duration(cfg.NonceTTLSeconds) * time.Second)
	defer nonceStore.Close()

	idempotencyStore := idempotency.NewStore(time.Duration(cfg.IdempotencyTTLSeconds) * time.Second)
	defer idempotencyStore.Close()

	notifier := webhook.NewNotifier(cfg.WebhookURL, logger)

	logger.Info("features",
		"orderless_enabled", cfg.OrderlessEnabled,
		"nonce_ttl_seconds", cfg.NonceTTLSeconds,
		"idempotency_ttl_seconds", cfg.IdempotencyTTLSeconds,
	)

	// Build router
	mux := http.NewServeMux()
	if aptosClient != nil && circleSigner != nil {
		mux.HandleFunc("POST /v1/execute", handler2.Execute(cfg, aptosClient, abiCache, circleSigner, memStore, nonceStore, idempotencyStore, logger))
	} else {
		mux.HandleFunc("POST /v1/execute", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"execute endpoint not configured (missing Circle or Aptos credentials)"}`))
		})
	}
	if aptosClient != nil {
		mux.HandleFunc("POST /v1/query", handler2.Query(aptosClient, abiCache))
	} else {
		mux.HandleFunc("POST /v1/query", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"query endpoint not configured (missing Aptos credentials)"}`))
		})
	}
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

	if aptosClient != nil {
		txnPoller := poller.New(aptosClient, memStore, notifier, time.Duration(cfg.PollIntervalSeconds)*time.Second, logger)
		go txnPoller.Run(ctx)
	}

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
