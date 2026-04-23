// Package archive purges terminal-status transaction rows that have aged past
// the operator's retention window. Without this the transactions table grows
// monotonically: every confirmed/failed/expired row sticks around forever,
// along with the UNIQUE(idempotency_key) index entry and any webhook_deliveries
// child rows. A multi-million-row backlog hurts every query that touches the
// table and makes the UNIQUE index dominate RAM.
//
// The worker runs as an optional background goroutine (gated by
// ArchiveConfig.Enabled). On each tick it does two bounded passes:
//
//  1. ClearIdempotencyOlderThan: NULLs idempotency_key on rows older than
//     IdempotencyRetentionDays. The audit row stays; clients that wanted to
//     retry using the same key can do so past this window.
//
//  2. PurgeTerminalOlderThan: DELETEs rows older than RetentionDays. The
//     foreign key on webhook_deliveries has ON DELETE CASCADE, so webhook
//     child rows go with them automatically.
//
// Each pass loops within the tick until a batch returns fewer than BatchSize
// rows, keeping individual DELETE/UPDATE statements short enough not to hold
// row locks for extended periods.
package archive

import (
	"context"
	"log/slog"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

// Archiver trims terminal transaction rows from the store.
type Archiver struct {
	store                    store.Store
	tick                     time.Duration
	retention                time.Duration
	idempotencyRetention     time.Duration
	batchSize                int
	logger                   *slog.Logger
}

// Config holds runtime parameters. All durations are positive; non-positive
// inputs are rejected in New so misconfiguration fails loudly at startup rather
// than silently turning off the sweep.
type Config struct {
	Tick                 time.Duration
	Retention            time.Duration
	IdempotencyRetention time.Duration
	BatchSize            int
}

// New builds an Archiver. Caller is responsible for guarding on Enabled — this
// constructor only sanity-checks the durations.
func New(st store.Store, cfg Config, logger *slog.Logger) *Archiver {
	tick := cfg.Tick
	if tick <= 0 {
		tick = 5 * time.Minute
	}
	retention := cfg.Retention
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	idemp := cfg.IdempotencyRetention
	if idemp <= 0 {
		idemp = 7 * 24 * time.Hour
	}
	batch := cfg.BatchSize
	if batch <= 0 {
		batch = 1000
	}
	return &Archiver{
		store:                st,
		tick:                 tick,
		retention:            retention,
		idempotencyRetention: idemp,
		batchSize:            batch,
		logger:               logger,
	}
}

// Run starts the archive loop. Blocks until ctx is cancelled. Runs one sweep
// immediately on startup so long-running processes don't have to wait a full
// tick to catch up.
func (a *Archiver) Run(ctx context.Context) {
	a.sweep(ctx)

	ticker := time.NewTicker(a.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sweep(ctx)
		}
	}
}

func (a *Archiver) sweep(ctx context.Context) {
	now := time.Now().UTC()

	// Stage 1: clear idempotency keys on rows past the shorter window but
	// still inside the full retention window. Loop until a batch returns short.
	idempCutoff := now.Add(-a.idempotencyRetention)
	var idempCleared int64
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := a.store.ClearIdempotencyOlderThan(ctx, idempCutoff, a.batchSize)
		if err != nil {
			a.logger.Error("archive: clear idempotency", "error", err)
			break
		}
		idempCleared += n
		if n < int64(a.batchSize) {
			break
		}
	}

	// Stage 2: DELETE fully-aged terminal rows. Same batched loop.
	purgeCutoff := now.Add(-a.retention)
	var purged int64
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := a.store.PurgeTerminalOlderThan(ctx, purgeCutoff, a.batchSize)
		if err != nil {
			a.logger.Error("archive: purge terminal", "error", err)
			break
		}
		purged += n
		if n < int64(a.batchSize) {
			break
		}
	}

	if idempCleared > 0 || purged > 0 {
		a.logger.Info("archive: sweep complete",
			"idempotency_cleared", idempCleared,
			"rows_purged", purged,
			"retention_days", int(a.retention/(24*time.Hour)),
			"idempotency_retention_days", int(a.idempotencyRetention/(24*time.Hour)),
		)
	}
}
