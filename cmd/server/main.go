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

	"github.com/aptos-labs/jc-contract-integration/internal/account"
	apiPkg "github.com/aptos-labs/jc-contract-integration/internal/api"
	"github.com/aptos-labs/jc-contract-integration/internal/api/handler"
	"github.com/aptos-labs/jc-contract-integration/internal/api/middleware"
	"github.com/aptos-labs/jc-contract-integration/internal/api/openapi"
	aptosint "github.com/aptos-labs/jc-contract-integration/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/internal/config"
	"github.com/aptos-labs/jc-contract-integration/internal/signer"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/aptos-labs/jc-contract-integration/internal/txn"
)

// / main starts an API server for the contract
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	// 1. Load config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger.Info("config loaded",
		"port", cfg.ServerPort,
		"signer_provider", cfg.SignerProvider,
		"aptos_node", cfg.AptosNodeURL,
		"testing_mode", cfg.TestingMode,
	)

	// 2. Initialize SQLite store
	st, err := store.NewSQLiteStore(cfg.SQLitePath)
	if err != nil {
		return fmt.Errorf("init store: %w", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Error("failed to close store", "error", err)
		}
	}()

	// 3. Initialize Aptos client
	client, err := aptosint.NewClient(cfg.AptosNodeURL, cfg.AptosChainID, int64(cfg.TxnExpirationSeconds), cfg.MaxGasAmount)
	if err != nil {
		return fmt.Errorf("init aptos client: %w", err)
	}

	// 4. Initialize signers and account registry
	registry, err := initRegistry(cfg)
	if err != nil {
		return fmt.Errorf("init registry: %w", err)
	}

	// 5. Initialize ABI cache for generic contract execution
	abiCache := aptosint.NewABICache(cfg.AptosNodeURL)

	// 6. Initialize transaction manager + poller
	mgr := txn.NewManager(client, st, registry, cfg.MaxRetries, cfg.TxnExpirationSeconds, cfg.MaxGasAmount, cfg.GasPerRecipient, logger)

	// Register operation factory for stuck transaction resubmission
	txn.RegisterOperationFactory("execute", handler.RebuildExecute(abiCache))

	poller := txn.NewPoller(client, st, time.Duration(cfg.PollIntervalSeconds)*time.Second,
		cfg.TxnExpirationSeconds, cfg.RetryBackoffBaseSeconds, cfg.RetryBackoffMaxSeconds, mgr, logger)

	// 7. Build HTTP router
	if cfg.TestingMode {
		logger.Warn("*** TESTING MODE ENABLED — authentication is disabled, do NOT use in production ***")
	}
	router := buildRouter(mgr, abiCache, cfg.AptosNodeURL, cfg.APIKey, cfg.TestingMode, logger)

	// 8. Start HTTP server with graceful shutdown
	srv := &http.Server{
		Addr:         ":" + cfg.ServerPort,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start poller
	go poller.Run(ctx)

	// Start server
	go func() {
		logger.Info("server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			cancel()
		}
	}()

	// Wait for shutdown signal
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

func initRegistry(cfg *config.Config) (*account.Registry, error) {
	registry := account.NewRegistry()

	switch cfg.SignerProvider {
	case "local":
		for i, hexKey := range cfg.SignerKeys {
			s, err := signer.NewLocalSigner(hexKey)
			if err != nil {
				return nil, fmt.Errorf("init local signer #%d: %w", i, err)
			}
			registry.Register(s)
			addr := s.Address()
			slog.Info("registered local signer", "address", addr.String())
		}
	case "circle":
		circleClient := signer.NewCircleClient(cfg.CircleAPIKey)

		for _, cw := range cfg.CircleWallets {
			if cw.WalletID == "" || cw.Address == "" {
				continue
			}
			pubKeyHex := cw.PublicKey
			if pubKeyHex == "" {
				// Public key not configured — fetch it from Circle at startup.
				walletResp, err := circleClient.GetWallet(context.Background(), cw.WalletID)
				if err != nil {
					return nil, fmt.Errorf("fetch public key for wallet %s: %w", cw.WalletID, err)
				}
				pubKeyHex = walletResp.Data.Wallet.InitialPublicKey
				if pubKeyHex == "" {
					return nil, fmt.Errorf("circle wallet %s has no initialPublicKey; set public_key in CIRCLE_WALLETS", cw.WalletID)
				}
			}
			if !strings.HasPrefix(pubKeyHex, "0x") {
				pubKeyHex = "0x" + pubKeyHex
			}
			s, err := signer.NewCircleSigner(
				circleClient,
				cw.WalletID,
				cfg.CircleEntitySecret,
				pubKeyHex,
				cw.Address,
			)
			if err != nil {
				return nil, fmt.Errorf("init circle signer for wallet %s: %w", cw.WalletID, err)
			}
			registry.Register(s)
			slog.Info("registered circle signer", "address", cw.Address, "wallet_id", cw.WalletID)
		}
	default:
		return nil, fmt.Errorf("unknown signer provider: %q (expected \"local\" or \"circle\")", cfg.SignerProvider)
	}

	return registry, nil
}

func buildRouter(mgr *txn.Manager, abiCache *aptosint.ABICache, nodeURL, apiKey string, testingMode bool, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Generic contract endpoints
	mux.HandleFunc("POST /v1/contracts/execute", handler.Execute(mgr, abiCache))
	mux.HandleFunc("POST /v1/contracts/query", handler.Query(nodeURL))

	// Transaction tracking
	mux.HandleFunc("GET /v1/transactions/{id}", handler.GetTransaction(mgr))

	// Health check (unauthenticated — placed before auth middleware is applied)
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		apiPkg.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// OpenAPI spec
	mux.HandleFunc("GET /v1/openapi.yaml", openapi.Handler())

	// Interactive API docs (Scalar)
	mux.HandleFunc("GET /v1/docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(docsHTML))
	})

	// Middleware stack (innermost applied first, outermost runs first)
	var h http.Handler = mux
	if !testingMode {
		h = middleware.Auth(apiKey)(h)
	}
	h = middleware.Recovery(logger)(h)
	h = middleware.Logging(logger)(h)
	h = middleware.RequestID(h)

	return h
}

const docsHTML = `<!doctype html>
<html>
<head>
  <title>Contract API Reference</title>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
</head>
<body>
  <div id="app"></div>
  <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
  <script>
    Scalar.createApiReference('#app', { url: '/v1/openapi.yaml' })
  </script>
</body>
</html>
`
