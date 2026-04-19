package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/joho/godotenv"

	"github.com/aptos-labs/jc-contract-integration/internal/db"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(); err != nil {
		logger.Error("migrate", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// .env is a convenience for local runs — ignored if missing. In CI the
	// DSN comes from the job environment directly.
	_ = godotenv.Load()

	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		return fmt.Errorf("MYSQL_DSN is required")
	}

	if err := db.Migrate(dsn); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	slog.Info("migrations applied", "target", dsnTarget(dsn))
	return nil
}

// dsnTarget strips credentials from a go-sql-driver DSN for safe logging —
// "user:pass@tcp(host:port)/db" → "tcp(host:port)/db". Best-effort: if the DSN
// doesn't contain an '@', returns an opaque placeholder to avoid leaking it.
func dsnTarget(dsn string) string {
	if _, after, ok := strings.Cut(dsn, "@"); ok {
		return after
	}
	return "<set>"
}
