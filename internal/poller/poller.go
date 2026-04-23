package poller

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/aptos-labs/aptos-go-sdk/api"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"golang.org/x/time/rate"
)

// aptosTxnClient is the slice of aptos.Client the poller depends on.
//
// TransactionByHashCtx wraps the underlying SDK's context-free
// TransactionByHash with context cancellation. The SDK's HTTP client has its
// own total timeout, but without a context check a slow or unresponsive node
// would pin this goroutine for the full HTTP timeout and stall the entire
// poll tick. Surfacing ctx here lets the poller drop the rest of the
// sweep cleanly on shutdown instead of waiting for the in-flight RPC.
type aptosTxnClient interface {
	TransactionByHashCtx(ctx context.Context, hash string) (*api.Transaction, error)
}

type notifyHook interface {
	Notify(ctx context.Context, rec *store.TransactionRecord)
}

// Poller polls the Aptos node for submitted transaction outcomes.
//
// Each poll cycle iterates over all submitted rows (plus processing rows with
// a pre-persisted hash, see Fix #1) and issues a TransactionByHash call for
// each one. With a large backlog that fan-out is a burst against the node
// that can trigger 429/503 and cascade into false "lookup error" log spam.
// A token-bucket rate limiter smooths the bursts; when it's nil, rate
// limiting is disabled.
//
// The sweep is paginated and parallelized: each tick pages through matching
// rows using ListByStatusPaged, and within each page a bounded worker pool
// runs confirmRecord in parallel. The rate limiter remains the real
// throughput ceiling — parallelism just prevents one slow node lookup from
// stalling every record behind it.
type Poller struct {
	client           aptosTxnClient
	store            store.Store
	notifier         notifyHook
	interval         time.Duration
	limiter          *rate.Limiter
	pageSize         int
	sweepConcurrency int
	logger           *slog.Logger
}

// New builds a Poller. rpcRPS > 0 enables a token-bucket limiter with the
// given steady-state rate and burst; rpcRPS == 0 disables limiting.
// pageSize bounds the rows loaded per ListByStatusPaged call; sweepConcurrency
// sizes the per-page worker pool.
func New(
	client aptosTxnClient,
	st store.Store,
	notifier notifyHook,
	interval time.Duration,
	rpcRPS, rpcBurst int,
	pageSize, sweepConcurrency int,
	logger *slog.Logger,
) *Poller {
	var limiter *rate.Limiter
	if rpcRPS > 0 {
		burst := rpcBurst
		if burst <= 0 {
			burst = rpcRPS
		}
		limiter = rate.NewLimiter(rate.Limit(rpcRPS), burst)
	}
	if pageSize <= 0 {
		pageSize = 500
	}
	if sweepConcurrency <= 0 {
		sweepConcurrency = 1
	}
	return &Poller{
		client:           client,
		store:            st,
		notifier:         notifier,
		interval:         interval,
		limiter:          limiter,
		pageSize:         pageSize,
		sweepConcurrency: sweepConcurrency,
		logger:           logger,
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
	// Primary path: records the submitter successfully flipped to submitted.
	p.sweepStatus(ctx, store.StatusSubmitted, false)

	// Recovery path: records whose post-submit status flip failed mid-flight.
	// The submitter pre-persists txn_hash before broadcast, so a processing
	// row with a non-empty hash has already hit the chain and must never be
	// re-signed. Confirm them by on-chain lookup here.
	p.sweepStatus(ctx, store.StatusProcessing, true)
}

// sweepStatus pages through all rows in the given status and fans confirmRecord
// out onto a bounded worker pool. requireHash skips records without a txn hash
// (used for the processing recovery path — only pre-persisted hashes are safe
// to confirm without re-signing).
func (p *Poller) sweepStatus(ctx context.Context, status store.TxnStatus, requireHash bool) {
	var (
		cursorTime time.Time
		cursorID   string
	)
	pageSize := p.pageSize
	if pageSize <= 0 {
		pageSize = 500
	}
	for {
		if ctx.Err() != nil {
			return
		}
		page, err := p.store.ListByStatusPaged(ctx, status, pageSize, cursorTime, cursorID)
		if err != nil {
			p.logger.Error("poller: list paged", "status", status, "error", err)
			return
		}
		if len(page) == 0 {
			return
		}

		// Advance the cursor to the last row of this page. ListByStatusPaged
		// orders strictly by (updated_at ASC, id ASC) so this is the
		// high-watermark for the next page.
		last := page[len(page)-1]
		cursorTime = last.UpdatedAt
		cursorID = last.ID

		p.processPage(ctx, page, status, requireHash)

		// A short page means we've drained the backlog for this status.
		// Stop here rather than issuing one more query that would return 0 rows.
		if len(page) < pageSize {
			return
		}
	}
}

// processPage runs confirmRecord across the page with bounded parallelism.
// Parallelism is free under the rate limiter — confirmRecord blocks on
// limiter.Wait before each RPC, so the throughput ceiling is unchanged.
// What we gain is that a slow lookup for record N doesn't block lookups for
// records N+1..N+p.sweepConcurrency in the same page.
func (p *Poller) processPage(ctx context.Context, page []*store.TransactionRecord, expected store.TxnStatus, requireHash bool) {
	concurrency := p.sweepConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, rec := range page {
		if requireHash && rec.TxnHash == "" {
			continue
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(r *store.TransactionRecord) {
			defer wg.Done()
			defer func() { <-sem }()
			p.confirmRecord(ctx, r, expected)
		}(rec)
	}
	wg.Wait()
}

// confirmRecord looks the record up on chain and transitions it to its
// terminal status. expected is the status the record was in when we read it;
// UpdateIfStatus uses it to avoid clobbering concurrent writes.
func (p *Poller) confirmRecord(ctx context.Context, rec *store.TransactionRecord, expected store.TxnStatus) {
	if rec.TxnHash == "" {
		if time.Now().UTC().After(rec.ExpiresAt) {
			rec.Status = store.StatusExpired
			rec.ErrorMessage = "transaction expired without submission hash"
			rec.UpdatedAt = time.Now().UTC()
			updated, err := p.store.UpdateIfStatus(ctx, rec, expected)
			if err != nil {
				p.logger.Error("poller: update expired", "txn_id", rec.ID, "error", err)
			}
			if updated {
				p.notifier.Notify(ctx, rec)
			}
		}
		return
	}
	if p.limiter != nil {
		if err := p.limiter.Wait(ctx); err != nil {
			// ctx cancelled mid-sweep; drop this record for this tick.
			return
		}
	}
	txn, err := p.client.TransactionByHashCtx(ctx, rec.TxnHash)
	if err != nil {
		if time.Now().UTC().After(rec.ExpiresAt) {
			rec.Status = store.StatusExpired
			rec.ErrorMessage = "transaction expired; on-chain lookup failed"
			rec.UpdatedAt = time.Now().UTC()
			updated, uerr := p.store.UpdateIfStatus(ctx, rec, expected)
			if uerr != nil {
				p.logger.Error("poller: update expired", "txn_id", rec.ID, "error", uerr)
			}
			if updated {
				p.notifier.Notify(ctx, rec)
			}
		} else {
			p.logger.Warn("poller: txn lookup error", "txn_id", rec.ID, "hash", rec.TxnHash, "error", err)
		}
		return
	}
	success := txn.Success()
	if success == nil {
		return
	}
	if *success {
		rec.Status = store.StatusConfirmed
	} else {
		rec.Status = store.StatusFailed
		rec.ErrorMessage = vmStatus(txn)
	}
	rec.UpdatedAt = time.Now().UTC()
	updated, err := p.store.UpdateIfStatus(ctx, rec, expected)
	if err != nil {
		p.logger.Error("poller: update", "txn_id", rec.ID, "status", rec.Status, "error", err)
		return
	}
	if updated {
		p.notifier.Notify(ctx, rec)
	}
}

// vmStatus extracts the VM status string from a committed transaction.
func vmStatus(txn *api.Transaction) string {
	if ut, ok := txn.Inner.(*api.UserTransaction); ok {
		return ut.VmStatus
	}
	return "unknown vm_status"
}
