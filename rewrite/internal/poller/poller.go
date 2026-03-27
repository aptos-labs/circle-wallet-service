package poller

import (
	"context"
	"log/slog"
	"time"

	"github.com/aptos-labs/aptos-go-sdk/api"
	aptosint "github.com/aptos-labs/jc-contract-integration/rewrite/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/rewrite/internal/store"
	"github.com/aptos-labs/jc-contract-integration/rewrite/internal/webhook"
)

type Poller struct {
	client   *aptosint.Client
	store    store.Store
	notifier *webhook.Notifier
	interval time.Duration
	logger   *slog.Logger
}

func New(client *aptosint.Client, st store.Store, notifier *webhook.Notifier, interval time.Duration, logger *slog.Logger) *Poller {
	return &Poller{
		client:   client,
		store:    st,
		notifier: notifier,
		interval: interval,
		logger:   logger,
	}
}

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
		// Check expiration first
		if time.Now().UTC().After(rec.ExpiresAt) {
			rec.Status = store.StatusExpired
			rec.ErrorMessage = "transaction expired on-chain"
			if err := p.store.Update(ctx, rec); err != nil {
				p.logger.Error("poller: update expired", "txn_id", rec.ID, "error", err)
			}
			p.notifier.Notify(rec)
			continue
		}
		if rec.TxnHash == "" {
			continue
		}
		// Query Aptos node for transaction status
		txn, err := p.client.TransactionByHash(rec.TxnHash)
		if err != nil {
			// Not found yet — keep polling
			continue
		}
		success := txn.Success()
		if success == nil {
			// Transaction is still pending
			continue
		}
		if *success {
			rec.Status = store.StatusConfirmed
			if err := p.store.Update(ctx, rec); err != nil {
				p.logger.Error("poller: update confirmed", "txn_id", rec.ID, "error", err)
			}
			p.notifier.Notify(rec)
		} else {
			rec.Status = store.StatusFailed
			rec.ErrorMessage = vmStatus(txn)
			if err := p.store.Update(ctx, rec); err != nil {
				p.logger.Error("poller: update failed", "txn_id", rec.ID, "error", err)
			}
			p.notifier.Notify(rec)
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
