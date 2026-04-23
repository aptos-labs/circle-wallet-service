package main

import (
	"context"
	"crypto/subtle"
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
	"github.com/aptos-labs/jc-contract-integration/internal/archive"
	circle2 "github.com/aptos-labs/jc-contract-integration/internal/circle"
	"github.com/aptos-labs/jc-contract-integration/internal/config"
	"github.com/aptos-labs/jc-contract-integration/internal/db"
	handler2 "github.com/aptos-labs/jc-contract-integration/internal/handler"
	"github.com/aptos-labs/jc-contract-integration/internal/poller"
	"github.com/aptos-labs/jc-contract-integration/internal/store/mysql"
	"github.com/aptos-labs/jc-contract-integration/internal/submitter"
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

// run is the real entrypoint. It's split out of main so tests can invoke the
// wiring logic without touching os.Exit / os.Stdout, and so any error here
// flows through main's structured logger.
func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Migrations run before opening the pooled connection so that schema errors
	// surface during startup rather than on the first request. The migration
	// driver opens its own short-lived connection.
	if err := db.Migrate(cfg.MySQLDSN()); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	sqlDB, err := db.Open(cfg.MySQLDSN())
	if err != nil {
		return fmt.Errorf("mysql open: %w", err)
	}
	defer func() {
		_ = sqlDB.Close()
	}()

	memStore := mysql.New(sqlDB)
	defer func() {
		_ = memStore.Close()
	}()

	logger.Info("config loaded",
		"port", cfg.ServerPort(),
		"aptos_node", cfg.AptosNodeURL(),
		"testing_mode", cfg.TestingMode(),
	)

	// Circle and Aptos clients are optional. If either set of credentials is
	// missing, the server still starts but the affected endpoints return 503.
	// This lets /v1/health and /v1/query (the latter only needs Aptos) work in
	// environments where Circle isn't configured, e.g. local read-only testing.
	var circleClient *circle2.Client
	var circleSigner *circle2.Signer
	if cfg.CircleAPIKey() != "" && cfg.CircleEntitySecret() != "" {
		circleClient = circle2.NewClient(cfg.CircleAPIKey())
		circleSigner = circle2.NewSigner(circleClient, cfg.CircleEntitySecret())
	} else {
		logger.Warn("Circle credentials not fully configured; execute endpoint will not submit transactions")
	}

	var pubkeyCache *circle2.PublicKeyCache
	if circleClient != nil {
		pubkeyCache = circle2.NewPublicKeyCache(circleClient)
	}

	var aptosClient *aptos.Client
	var abiCache *aptos.ABICache
	if cfg.AptosNodeURL() != "" {
		aptosClient, err = aptos.NewClient(cfg.AptosNodeURL(), cfg.AptosChainID(), int64(cfg.TxnExpirationSeconds()), cfg.MaxGasAmount(), cfg.AptosAPIKey())
		if err != nil {
			return fmt.Errorf("init aptos client: %w", err)
		}
		abiCache = aptos.NewABICache(aptosClient.Inner)
	} else {
		logger.Warn("APTOS_NODE_URL not configured; query endpoint will not work")
	}

	notifier := webhook.NewWebhookNotifier(cfg.WebhookURL(), memStore, logger)

	webhookWorker := webhook.NewWorker(
		memStore,
		cfg.WebhookMaxRetries(),
		time.Duration(cfg.WebhookTimeoutSeconds())*time.Second,
		cfg.WebhookDeliveryConcurrency(),
		cfg.WebhookSigningSecret(),
		logger,
	)

	// A single cancellable context drives shutdown of every background goroutine.
	// SIGINT/SIGTERM cancels the context; each loop (submitter, poller, webhook,
	// HTTP server goroutine) observes ctx.Done() and exits. The HTTP server itself
	// is shut down with a bounded timeout below so that in-flight requests get a
	// chance to finish before the process exits.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Submitter requires the full triple (Aptos client, Circle signer, ABI cache).
	// Without any one of them, /v1/execute would have nothing to sign or submit,
	// so we skip spawning the worker entirely rather than have it loop over errors.
	if aptosClient != nil && circleSigner != nil && abiCache != nil {
		sub := submitter.New(cfg, memStore, aptosClient, abiCache, circleSigner, pubkeyCache, notifier, logger)
		go sub.Run(ctx)
	}

	// Poller only needs the Aptos client — it confirms already-submitted txns by
	// hash and doesn't sign anything. Safe to run even when Circle is absent.
	if aptosClient != nil {
		txnPoller := poller.New(
			aptosClient,
			memStore,
			notifier,
			time.Duration(cfg.PollIntervalSeconds())*time.Second,
			cfg.PollerRPCRequestsPerSecond(),
			cfg.PollerRPCBurst(),
			cfg.PollerPageSize(),
			cfg.PollerSweepConcurrency(),
			logger,
		)
		go txnPoller.Run(ctx)
	}

	go webhookWorker.Run(ctx)

	// Archive worker (optional, off by default). Bounds transactions-table
	// growth by deleting terminal rows older than the retention window and
	// NULLing idempotency keys after a shorter window so UNIQUE slots are
	// freed up earlier than the full-row purge.
	if cfg.ArchiveEnabled() {
		day := 24 * time.Hour
		archiver := archive.New(memStore, archive.Config{
			Tick:                 time.Duration(cfg.ArchiveTickSeconds()) * time.Second,
			Retention:            time.Duration(cfg.ArchiveRetentionDays()) * day,
			IdempotencyRetention: time.Duration(cfg.ArchiveIdempotencyRetentionDays()) * day,
			BatchSize:            cfg.ArchiveBatchSize(),
		}, logger)
		go archiver.Run(ctx)
	}

	mux := http.NewServeMux()
	if aptosClient != nil && circleSigner != nil {
		mux.HandleFunc("POST /v1/execute", handler2.Execute(cfg, memStore, pubkeyCache, logger))
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
	mux.HandleFunc("GET /v1/transactions/{id}/webhooks", handler2.ListWebhookDeliveries(memStore))
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("deep") == "1" {
			if err := sqlDB.PingContext(r.Context()); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"status":"error","db":"unreachable"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok","db":"ok"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Middleware chain is applied in reverse order of wrapping: outermost (auth)
	// runs first, then rate limit, then the mux. Auth is wrapped last so the
	// health check bypass below can route directly to mux without touching
	// either middleware.
	var inner http.Handler = mux
	if cfg.RateLimitEnabled() {
		// Per-wallet limiting isn't implemented; Config.validate rejects
		// rate_limit.per_wallet=true at startup so we don't pass it through.
		rl := handler2.NewRateLimitMiddleware(handler2.RateLimiterConfig{
			Enabled:           true,
			RequestsPerSecond: cfg.RateLimitRequestsPerSecond(),
			Burst:             cfg.RateLimitBurst(),
		})
		inner = rl.Wrap(inner)
	}

	// API-key auth is disabled in testing mode so the test suite doesn't need to
	// thread credentials through every request. In all other modes, every
	// endpoint except /v1/health requires a bearer token (or X-API-Key header)
	// matching cfg.APIKey(). Compared with subtle.ConstantTimeCompare to avoid
	// timing side channels. /v1/health bypasses auth so orchestrators can probe
	// liveness without a secret.
	h := inner
	if !cfg.TestingMode() {
		apiKeyBytes := []byte(cfg.APIKey())
		authedInner := inner
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/health" {
				mux.ServeHTTP(w, r)
				return
			}
			auth := r.Header.Get("Authorization")
			if auth == "" {
				auth = r.Header.Get("X-API-Key")
			}
			key := extractBearerToken(auth)
			if subtle.ConstantTimeCompare([]byte(key), apiKeyBytes) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			authedInner.ServeHTTP(w, r)
		})
	}

	srv := &http.Server{
		Addr:         ":" + cfg.ServerPort(),
		Handler:      h,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
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

	// Graceful HTTP shutdown: new connections are rejected, in-flight requests
	// have up to 10s to finish. Background goroutines (submitter, poller,
	// webhook) are already draining via the cancelled ctx. A fresh
	// context.Background() is used here because ctx itself is already cancelled.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}
	logger.Info("server stopped")
	return nil
}

// extractBearerToken pulls the token from an "Authorization: Bearer <token>"
// header. If the header doesn't use the Bearer scheme it's returned as-is,
// which lets X-API-Key (raw token) work through the same code path.
func extractBearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) >= len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return header[len(prefix):]
	}
	return header
}
