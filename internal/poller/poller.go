// Package poller confirms submitted transactions by polling the Aptos node.
//
// The [Poller] periodically lists all "submitted" transactions from the store,
// looks up each by txn_hash on-chain, and transitions them to "confirmed",
// "failed", or "expired" based on the result. Updates use [store.Store.UpdateIfStatus]
// (conditional on status = "submitted") so multiple server instances can poll
// concurrently without double-processing.
package poller

import (
	"context"
	"log/slog"
	"time"

	"github.com/aptos-labs/aptos-go-sdk/api"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

type aptosTxnClient interface {
	TransactionByHash(hash string) (*api.Transaction, error)
}

type notifyHook interface {
	Notify(ctx context.Context, rec *store.TransactionRecord)
}

// Poller polls the Aptos node for submitted transaction outcomes.
type Poller struct {
	client   aptosTxnClient
	store    store.Store
	notifier notifyHook
	interval time.Duration
	logger   *slog.Logger
}

func New(client aptosTxnClient, st store.Store, notifier notifyHook, interval time.Duration, logger *slog.Logger) *Poller {
	return &Poller{
		client:   client,
		store:    st,
		notifier: notifier,
		interval: interval,
		logger:   logger,
	}
}

// Run starts the polling loop. It blocks until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	records, err := p.store.ListByStatus(ctx, store.StatusSubmitted)
	if err != nil {
		p.logger.Error("poller: list submitted", "error", err)
		return
	}
	for _, rec := range records {
		if rec.TxnHash == "" {
			if time.Now().UTC().After(rec.ExpiresAt) {
				rec.Status = store.StatusExpired
				rec.ErrorMessage = "transaction expired without submission hash"
				rec.UpdatedAt = time.Now().UTC()
				updated, err := p.store.UpdateIfStatus(ctx, rec, store.StatusSubmitted)
				if err != nil {
					p.logger.Error("poller: update expired", "txn_id", rec.ID, "error", err)
				}
				if updated {
					p.notifier.Notify(ctx, rec)
				}
			}
			continue
		}
		txn, err := p.client.TransactionByHash(rec.TxnHash)
		if err != nil {
			if time.Now().UTC().After(rec.ExpiresAt) {
				rec.Status = store.StatusExpired
				rec.ErrorMessage = "transaction expired; on-chain lookup failed"
				rec.UpdatedAt = time.Now().UTC()
				updated, uerr := p.store.UpdateIfStatus(ctx, rec, store.StatusSubmitted)
				if uerr != nil {
					p.logger.Error("poller: update expired", "txn_id", rec.ID, "error", uerr)
				}
				if updated {
					p.notifier.Notify(ctx, rec)
				}
			} else {
				p.logger.Warn("poller: txn lookup error", "txn_id", rec.ID, "hash", rec.TxnHash, "error", err)
			}
			continue
		}
		success := txn.Success()
		if success == nil {
			continue
		}
		if *success {
			rec.Status = store.StatusConfirmed
		} else {
			rec.Status = store.StatusFailed
			rec.ErrorMessage = vmStatus(txn)
		}
		rec.UpdatedAt = time.Now().UTC()
		updated, err := p.store.UpdateIfStatus(ctx, rec, store.StatusSubmitted)
		if err != nil {
			p.logger.Error("poller: update", "txn_id", rec.ID, "status", rec.Status, "error", err)
			continue
		}
		if updated {
			p.notifier.Notify(ctx, rec)
		}
	}
}

// vmStatus extracts the VM status string from a committed transaction.
func vmStatus(txn *api.Transaction) string {
	if ut, ok := txn.Inner.(*api.UserTransaction); ok {
		return ut.VmStatus
	}
	return "unknown vm_status"
}
