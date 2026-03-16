package txn

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/aptos-labs/aptos-go-sdk/api"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

// Poller periodically checks submitted transactions for confirmation and retries failures.
type Poller struct {
	client        TxnClient
	store         store.Store
	interval      time.Duration
	expirationSec int
	backoffBase   int
	backoffMax    int
	submitter     *Manager
	logger        *slog.Logger
}

// NewPoller creates a background transaction status poller.
func NewPoller(
	client TxnClient,
	st store.Store,
	interval time.Duration,
	expirationSec int,
	backoffBase int,
	backoffMax int,
	submitter *Manager,
	logger *slog.Logger,
) *Poller {
	return &Poller{
		client:        client,
		store:         st,
		interval:      interval,
		expirationSec: expirationSec,
		backoffBase:   backoffBase,
		backoffMax:    backoffMax,
		submitter:     submitter,
		logger:        logger,
	}
}

// Run starts the polling loop. It blocks until the context is canceled.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("poller shutting down")
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	p.checkSubmitted(ctx)
	p.processRetryable(ctx)
	p.resubmitPending(ctx)
}

func (p *Poller) checkSubmitted(ctx context.Context) {
	records, err := p.store.ListByStatus(ctx, store.StatusSubmitted, 100)
	if err != nil {
		p.logger.Error("list submitted transactions", "error", err)
		return
	}

	for _, rec := range records {
		// Check if past expiration
		if time.Now().UTC().After(rec.ExpiresAt) {
			rec.Status = store.StatusExpired
			rec.ErrorMessage = "transaction expired"
			if err := p.store.UpdateTransaction(ctx, rec); err != nil {
				p.logger.Error("mark expired", "txn_id", rec.ID, "error", err)
			}
			p.logger.Info("transaction expired", "txn_id", rec.ID, "txn_hash", rec.TxnHash)
			continue
		}

		if rec.TxnHash == "" {
			continue
		}

		// Query Aptos for transaction status
		txn, err := p.client.TransactionByHash(rec.TxnHash)
		if err != nil {
			// Not found is expected for pending transactions — keep polling
			if isNotFound(err) {
				continue
			}
			p.logger.Error("query transaction", "txn_id", rec.ID, "txn_hash", rec.TxnHash, "error", err)
			continue
		}

		p.handleTxnResult(ctx, rec, txn)
	}
}

func (p *Poller) handleTxnResult(ctx context.Context, rec *store.TransactionRecord, txn *api.Transaction) {
	success := txn.Success()
	if success == nil {
		// Still pending
		return
	}

	if *success {
		rec.Status = store.StatusConfirmed
		rec.ErrorMessage = ""
		if err := p.store.UpdateTransaction(ctx, rec); err != nil {
			p.logger.Error("mark confirmed", "txn_id", rec.ID, "error", err)
		}
		p.logger.Info("transaction confirmed", "txn_id", rec.ID, "txn_hash", rec.TxnHash)
	} else {
		rec.Status = store.StatusFailed
		rec.ErrorMessage = vmStatusFromTxn(txn)
		if err := p.store.UpdateTransaction(ctx, rec); err != nil {
			p.logger.Error("mark failed", "txn_id", rec.ID, "error", err)
		}
		p.logger.Warn("transaction failed on-chain", "txn_id", rec.ID, "txn_hash", rec.TxnHash, "vm_status", rec.ErrorMessage)
	}
}

func (p *Poller) processRetryable(ctx context.Context) {
	records, err := p.store.ListRetryable(ctx, 50)
	if err != nil {
		p.logger.Error("list retryable transactions", "error", err)
		return
	}

	for _, rec := range records {
		if rec.Attempt >= rec.MaxRetries {
			rec.Status = store.StatusPermanentlyFailed
			if err := p.store.UpdateTransaction(ctx, rec); err != nil {
				p.logger.Error("mark permanently failed", "txn_id", rec.ID, "error", err)
			}
			continue
		}

		p.retry(ctx, rec)
	}
}

func (p *Poller) retry(ctx context.Context, rec *store.TransactionRecord) {
	p.logger.Info("retrying transaction", "txn_id", rec.ID, "attempt", rec.Attempt+1)

	rec.Attempt++
	rec.Status = store.StatusPending
	rec.ErrorMessage = ""
	rec.ExpiresAt = time.Now().UTC().Add(time.Duration(p.expirationSec) * time.Second)

	// Exponential backoff: base * 2^(attempt-1), capped at max
	delay := p.backoffBase
	for i := 1; i < rec.Attempt; i++ {
		delay *= 2
		if delay > p.backoffMax {
			delay = p.backoffMax
			break
		}
	}
	rec.RetryAfter = time.Now().UTC().Add(time.Duration(delay) * time.Second)

	if err := p.store.UpdateTransaction(ctx, rec); err != nil {
		p.logger.Error("update for retry", "txn_id", rec.ID, "error", err)
	}
}

func (p *Poller) resubmitPending(ctx context.Context) {
	if p.submitter == nil {
		return
	}

	records, err := p.store.ListPendingRetries(ctx, 10)
	if err != nil {
		p.logger.Error("list pending retries", "error", err)
		return
	}

	for _, rec := range records {
		op, err := RebuildOperation(rec)
		if err != nil {
			p.logger.Error("rebuild operation for resubmit", "txn_id", rec.ID, "error", err)
			rec.Status = store.StatusPermanentlyFailed
			rec.ErrorMessage = "rebuild failed: " + err.Error()
			if updateErr := p.store.UpdateTransaction(ctx, rec); updateErr != nil {
				p.logger.Error("mark permanently failed", "txn_id", rec.ID, "error", updateErr)
			}
			continue
		}

		if err := p.submitter.Resubmit(ctx, rec, op); err != nil {
			p.logger.Error("resubmit transaction", "txn_id", rec.ID, "error", err)
		}
	}
}

func isNotFound(err error) bool {
	return strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found")
}

func vmStatusFromTxn(txn *api.Transaction) string {
	if txn.Inner == nil {
		return "unknown"
	}
	// Try to extract VmStatus from UserTransaction type
	if ut, ok := txn.Inner.(*api.UserTransaction); ok {
		return ut.VmStatus
	}
	return "unknown"
}
