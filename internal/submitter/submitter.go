package submitter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
	"github.com/aptos-labs/jc-contract-integration/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/internal/circle"
	"github.com/aptos-labs/jc-contract-integration/internal/config"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

// Notifier is called when a transaction reaches a terminal status so that a
// webhook delivery can be queued. Implemented by [webhook.Notifier].
type Notifier interface {
	Notify(ctx context.Context, rec *store.TransactionRecord)
}

// transactionSubmitter is the minimal slice of aptos.Client the consumer loop
// depends on. Factored out so tests can inject a mock node without spinning up
// the real HTTP client.
type transactionSubmitter interface {
	SubmitTransaction(signed *aptossdk.SignedTransaction) (*api.SubmitTransactionResponse, error)
}

// signedItem is the unit of work passed from the signing producer to the
// submitting consumer. It carries the original DB row (for status updates),
// the fully signed transaction ready to POST, and the sequence number the
// signing pipeline used — kept on the item so we can log it even after the
// row's SequenceNumber field is cleared on failure.
type signedItem struct {
	rec       *store.TransactionRecord
	signedTxn *aptossdk.SignedTransaction
	seqNum    uint64
}

// Submitter is the top-level dispatcher that owns per-sender workers.
//
// The "one worker per sender address" invariant lives in Submitter.Run via the
// workers map; every per-sender correctness property (FIFO order, sequence
// allocation monotonicity, ShiftSenderSequences) depends on this.
type Submitter struct {
	cfg      *config.Config
	queue    store.Queue
	client   *aptos.Client
	txSubmit transactionSubmitter
	abi      *aptos.ABICache
	signer   *circle.Signer
	pkCache  *circle.PublicKeyCache
	notifier Notifier
	logger   *slog.Logger
}

func New(
	cfg *config.Config,
	q store.Queue,
	client *aptos.Client,
	abi *aptos.ABICache,
	signer *circle.Signer,
	pkCache *circle.PublicKeyCache,
	notifier Notifier,
	logger *slog.Logger,
) *Submitter {
	return &Submitter{
		cfg:      cfg,
		queue:    q,
		client:   client,
		abi:      abi,
		signer:   signer,
		pkCache:  pkCache,
		notifier: notifier,
		logger:   logger,
	}
}

// Run starts the dispatcher loop. It blocks until ctx is cancelled.
//
// Each tick, ListQueuedSenders returns every sender with outstanding queued
// work. For any sender that doesn't already have a live worker, a new
// runSenderWorker goroutine is spawned with a cancellable child context. The
// workers map doubles as a "is a worker already running for this sender"
// registry — this is how the single-worker-per-sender invariant is enforced.
//
// On shutdown (ctx cancelled), every worker's context is cancelled so they
// drain in-flight work and exit; Run itself returns immediately without
// waiting for them. Callers that need to wait for clean drain should close
// sqlDB only after all referenced goroutines have observed ctx.Done().
func (s *Submitter) Run(ctx context.Context) {
	go s.recoverLoop(ctx)

	ticker := time.NewTicker(time.Duration(s.cfg.SubmitterPollIntervalMs()) * time.Millisecond)
	defer ticker.Stop()

	workers := make(map[string]context.CancelFunc)
	var mu sync.Mutex

	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			for _, cancel := range workers {
				cancel()
			}
			mu.Unlock()
			return
		case <-ticker.C:
			senders, err := s.queue.ListQueuedSenders(ctx)
			if err != nil {
				s.logger.Error("submitter: list senders", "error", err)
				continue
			}
			mu.Lock()
			for _, sender := range senders {
				// Skip senders that already have a live worker. The worker's
				// deferred delete(workers, addr) below clears this entry when
				// the worker exits (queue drained or failure), at which point
				// a subsequent tick will spawn a fresh one if new work appears.
				if _, running := workers[sender]; running {
					continue
				}
				workerCtx, cancel := context.WithCancel(ctx)
				workers[sender] = cancel
				go func(addr string) {
					s.runSenderWorker(workerCtx, addr)
					mu.Lock()
					delete(workers, addr)
					mu.Unlock()
				}(sender)
			}
			mu.Unlock()
		}
	}
}

// recoverLoop periodically rescues transactions stuck in "processing" past the
// stale threshold. A row can end up stranded if a worker was killed (SIGKILL,
// OOM, node failure) between claiming the row and either submitting or
// requeuing it. RecoverStaleProcessing flips them back to queued and
// decrements the sequence counter to keep allocation contiguous.
func (s *Submitter) recoverLoop(ctx context.Context) {
	t := time.NewTicker(time.Duration(s.cfg.SubmitterRecoveryTickSeconds()) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			staleThreshold := time.Duration(s.cfg.SubmitterStaleProcessingSeconds()) * time.Second
			n, err := s.queue.RecoverStaleProcessing(ctx, staleThreshold)
			if err != nil {
				s.logger.Error("submitter: recover stale processing", "error", err)
				continue
			}
			if n > 0 {
				s.logger.Info("submitter: recovered stale processing rows", "count", n)
			}
		}
	}
}

// runSenderWorker runs the per-sender signing-and-submission pipeline.
//
// The producer (pipelineProducer) claims rows one at a time, signs them via
// Circle, and pushes fully-signed transactions onto the buffered channel. The
// consumer (this function's for-range loop) drains the channel and submits
// each one. This overlaps Circle signing latency with Aptos submission
// latency — the pipeline-depth knob controls how many sign-ahead transactions
// can be in flight.
//
// On any submit failure we pipeCancel the producer (so it stops claiming more
// work and creating more processing-state rows), drain whatever's left in the
// channel, and sweep any stray processing rows the producer may have committed
// in the race window. This keeps the DB state clean for the next dispatcher
// tick, which will spawn a fresh worker with a fresh pipeline.
func (s *Submitter) runSenderWorker(ctx context.Context, senderAddress string) {
	depth := s.cfg.SubmitterSigningPipelineDepth()
	if depth < 1 {
		depth = 1
	}

	// A buffered channel of depth N gives us N-deep sign-ahead: the producer
	// blocks when N signed transactions are waiting to be submitted, providing
	// natural back-pressure when Aptos is slow.
	pipeline := make(chan signedItem, depth)
	pipeCtx, pipeCancel := context.WithCancel(ctx)
	defer pipeCancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(pipeline)
		s.pipelineProducer(pipeCtx, senderAddress, pipeline)
	}()

	failed := false
	for item := range pipeline {
		if !s.submitSigned(ctx, &item) {
			failed = true
			pipeCancel()
			break
		}
	}

	if failed {
		s.drainPipeline(ctx, pipeline, senderAddress)
	}

	wg.Wait()

	s.sweepOrphanedProcessing(ctx, senderAddress)
}

// pipelineProducer is the signing half of the per-sender pipeline. It loops
// claiming queued rows, preparing+signing them, and pushing them onto the
// output channel. Returns when:
//   - ctx is cancelled (worker shutting down, or submit failure triggered pipeCancel),
//   - ClaimNextQueuedForSender returns (nil, nil) — no more queued work,
//   - the channel send blocks and ctx is cancelled mid-send.
//
// On the last case, the pre-signed record is requeued and its sequence number
// released so it can be re-allocated by the next worker invocation.
func (s *Submitter) pipelineProducer(ctx context.Context, senderAddress string, out chan<- signedItem) {
	for {
		if ctx.Err() != nil {
			return
		}

		rec, err := s.queue.ClaimNextQueuedForSender(ctx, senderAddress)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.Error("submitter: claim for sender", "sender", senderAddress, "error", err)
			s.retrySleep(ctx)
			continue
		}
		if rec == nil {
			return
		}

		item, transient := s.prepareRecord(ctx, rec)
		if item == nil {
			if transient {
				s.retrySleep(ctx)
			}
			continue
		}

		select {
		case out <- *item:
		case <-ctx.Done():
			s.requeueRecord(ctx, rec)
			return
		}
	}
}

// prepareRecord builds and signs a transaction. Returns (item, false) on
// success, (nil, false) on permanent failure, (nil, true) on transient failure.
//
// Work performed, in order:
//  1. Expiration and max-retry-duration checks — cheap; fail fast if the row
//     has aged out before we spend on Circle signing.
//  2. Resolve the sender's Circle wallet public key (cached).
//  3. Decode the payload JSON and build a Move entry-function payload via the
//     ABI cache.
//  4. Construct a fee-payer RawTransaction at the row's allocated sequence.
//  5. Call Circle to sign as the sender; if a separate fee payer is set, sign
//     again as the fee payer.
//  6. Assemble the FeePayerSignedTransaction.
//
// Any step that fails with a network/service error is classified transient
// and triggers a requeue + ReleaseSequence. Validation or wallet errors are
// permanent and trigger ShiftSenderSequences + webhook notify.
func (s *Submitter) prepareRecord(ctx context.Context, rec *store.TransactionRecord) (*signedItem, bool) {
	now := time.Now().UTC()

	if now.After(rec.ExpiresAt) {
		s.markPermanentFailure(ctx, rec, "transaction expired before submit")
		return nil, false
	}

	maxDuration := time.Duration(s.cfg.SubmitterMaxRetryDurationSeconds()) * time.Second
	if time.Since(rec.CreatedAt) > maxDuration {
		s.markPermanentFailure(ctx, rec, "max retry duration exceeded")
		return nil, false
	}

	if rec.SequenceNumber == nil {
		s.requeueTransient(ctx, rec, fmt.Errorf("no sequence number allocated"))
		return nil, true
	}
	useSeq := *rec.SequenceNumber

	wallet, err := resolveWallet(ctx, s.pkCache, rec)
	if err != nil {
		s.markPermanentFailure(ctx, rec, err.Error())
		return nil, false
	}
	if err := wallet.VerifyWallet(); err != nil {
		s.markPermanentFailure(ctx, rec, "invalid wallet: "+err.Error())
		return nil, false
	}

	var qp store.QueuedPayload
	if err := json.Unmarshal([]byte(rec.PayloadJSON), &qp); err != nil {
		s.markPermanentFailure(ctx, rec, "bad payload_json: "+err.Error())
		return nil, false
	}

	entry, err := s.abi.BuildEntryFunctionPayload(rec.FunctionID, qp.TypeArguments, qp.Arguments)
	if err != nil {
		s.markPermanentFailure(ctx, rec, err.Error())
		return nil, false
	}

	senderAddr, err := aptos.ParseAddress(wallet.Address)
	if err != nil {
		s.markPermanentFailure(ctx, rec, err.Error())
		return nil, false
	}

	var maxGas uint64
	if rec.MaxGasAmount != nil {
		maxGas = *rec.MaxGasAmount
	}

	payload := aptossdk.TransactionPayload{Payload: entry}

	hasSeparateFeePayer := rec.FeePayerWalletID != "" && rec.FeePayerAddress != ""

	var feePayerAddr aptossdk.AccountAddress
	if hasSeparateFeePayer {
		feePayerAddr, err = aptos.ParseAddress(rec.FeePayerAddress)
		if err != nil {
			s.markPermanentFailure(ctx, rec, "invalid fee_payer_address: "+err.Error())
			return nil, false
		}
	} else {
		feePayerAddr = senderAddr
	}

	rawTxn, err := s.client.BuildFeePayerTransaction(senderAddr, feePayerAddr, payload, maxGas, useSeq)
	if err != nil {
		s.requeueTransient(ctx, rec, err)
		return nil, true
	}

	senderAuth, err := s.signer.SignTransaction(ctx, rawTxn, wallet.WalletID, wallet.PublicKey)
	if err != nil {
		s.requeueTransient(ctx, rec, err)
		return nil, true
	}

	var feePayerAuth *crypto.AccountAuthenticator
	if hasSeparateFeePayer {
		fpPubKey, err := s.pkCache.Resolve(ctx, rec.FeePayerWalletID)
		if err != nil {
			s.requeueTransient(ctx, rec, fmt.Errorf("resolve fee-payer public key: %w", err))
			return nil, true
		}
		feePayerAuth, err = s.signer.SignTransaction(ctx, rawTxn, rec.FeePayerWalletID, fpPubKey)
		if err != nil {
			s.requeueTransient(ctx, rec, fmt.Errorf("fee-payer sign: %w", err))
			return nil, true
		}
	} else {
		feePayerAuth = senderAuth
	}

	signedTxn, ok := rawTxn.ToFeePayerSignedTransaction(senderAuth, feePayerAuth, []crypto.AccountAuthenticator{})
	if !ok {
		s.markPermanentFailure(ctx, rec, "failed to assemble signed transaction")
		return nil, false
	}

	return &signedItem{rec: rec, signedTxn: signedTxn, seqNum: useSeq}, false
}

// submitSigned POSTs a signed transaction to the Aptos node and updates the DB
// row to status=submitted on success. Returns true on success (continue the
// pipeline), false on any failure (caller cancels the pipeline and drains).
//
// Three failure modes are distinguished:
//   - Sequence mismatch (detected by isSequenceError) → reconcileAndRequeue:
//     fetch chain's current sequence, bump the counter, requeue this record
//     and any processing siblings so they can be re-signed.
//   - Aged-out error (past max_retry_duration) → permanent failure.
//   - Anything else → transient; requeue and try again on the next tick.
func (s *Submitter) submitSigned(ctx context.Context, item *signedItem) bool {
	sub := s.txSubmit
	if sub == nil {
		sub = s.client
	}
	submitResp, err := sub.SubmitTransaction(item.signedTxn)
	if err != nil {
		if isSequenceError(err) {
			senderAddr, parseErr := aptos.ParseAddress(item.rec.SenderAddress)
			if parseErr == nil {
				s.reconcileAndRequeue(ctx, item.rec, senderAddr)
			} else {
				s.requeueTransient(ctx, item.rec, err)
			}
			return false
		}
		maxDuration := time.Duration(s.cfg.SubmitterMaxRetryDurationSeconds()) * time.Second
		if time.Since(item.rec.CreatedAt) > maxDuration {
			s.markPermanentFailure(ctx, item.rec, err.Error())
		} else {
			s.requeueTransient(ctx, item.rec, err)
		}
		return false
	}

	item.rec.Status = store.StatusSubmitted
	item.rec.TxnHash = submitResp.Hash
	item.rec.UpdatedAt = time.Now().UTC()
	item.rec.LastError = ""
	if err := s.queue.Update(ctx, item.rec); err != nil {
		s.logger.Error("submitter: update submitted", "id", item.rec.ID, "error", err)
		return false
	}

	s.logger.Info("submitter: submitted", "id", item.rec.ID, "hash", item.rec.TxnHash, "sequence", item.seqNum)
	return true
}

// markPermanentFailure terminates a transaction with status=failed and fires
// a webhook. Because this record held a sequence number that will never be
// used on chain, ShiftSenderSequences slides every higher-numbered sibling
// back to queued so they'll be re-claimed with fresh sequences — this
// prevents a permanent gap in the per-sender sequence stream that would block
// every subsequent submit for that account.
func (s *Submitter) markPermanentFailure(ctx context.Context, rec *store.TransactionRecord, msg string) {
	rec.Status = store.StatusFailed
	rec.ErrorMessage = msg
	rec.UpdatedAt = time.Now().UTC()
	if err := s.queue.Update(ctx, rec); err != nil {
		s.logger.Error("submitter: mark failed", "id", rec.ID, "error", err)
	}
	if rec.SequenceNumber != nil {
		if err := s.queue.ShiftSenderSequences(ctx, rec.SenderAddress, *rec.SequenceNumber); err != nil {
			s.logger.Error("submitter: shift sequences", "sender", rec.SenderAddress, "error", err)
		}
	}
	s.notifier.Notify(ctx, rec)
}

// requeueTransient puts a record back in queued state after a recoverable
// failure (signing blip, submit network error). Clears the allocated sequence
// number and, if we had claimed one, releases the counter by 1 so the next
// claim reuses that slot instead of creating a gap.
//
// attempt_count is incremented for observability; there's no attempt cap here
// — the wall-clock max_retry_duration check in prepareRecord / submitSigned
// is what caps retries.
func (s *Submitter) requeueTransient(ctx context.Context, rec *store.TransactionRecord, err error) {
	s.logger.Warn("submitter: retry", "id", rec.ID, "error", err)
	hadSequence := rec.SequenceNumber != nil
	rec.Status = store.StatusQueued
	rec.SequenceNumber = nil
	rec.AttemptCount++
	rec.LastError = err.Error()
	rec.UpdatedAt = time.Now().UTC()
	if err2 := s.queue.Update(ctx, rec); err2 != nil {
		s.logger.Error("submitter: requeue", "id", rec.ID, "error", err2)
	}
	if hadSequence {
		if err2 := s.queue.ReleaseSequence(ctx, rec.SenderAddress); err2 != nil {
			s.logger.Error("submitter: release sequence", "id", rec.ID, "error", err2)
		}
	}
}

// requeueRecord puts a record back in queued without bumping attempt_count.
// Used for "orderly give-backs" — pipeline drain on shutdown, or sweeping
// orphaned processing rows — where no real attempt was made, so counting it
// as a retry would inflate the attempt metric misleadingly.
func (s *Submitter) requeueRecord(ctx context.Context, rec *store.TransactionRecord) {
	hadSequence := rec.SequenceNumber != nil
	rec.Status = store.StatusQueued
	rec.SequenceNumber = nil
	rec.UpdatedAt = time.Now().UTC()
	if err := s.queue.Update(ctx, rec); err != nil {
		s.logger.Error("submitter: requeue on cancel", "id", rec.ID, "error", err)
	}
	if hadSequence {
		if err := s.queue.ReleaseSequence(ctx, rec.SenderAddress); err != nil {
			s.logger.Error("submitter: release sequence on cancel", "id", rec.ID, "error", err)
		}
	}
}

// reconcileAndRequeue handles a sequence-number mismatch from the Aptos node.
//
// Causes, in decreasing order of likelihood:
//  1. Another client submitted a transaction for this account outside our
//     service, advancing the chain's sequence.
//  2. We restarted after a crash and the DB counter is now behind chain state.
//  3. A prior permanent failure left a gap we haven't closed yet.
//
// Resolution:
//  1. Fetch the current sequence from the node.
//  2. ReconcileSequence raises our counter to max(ours, chain).
//  3. Requeue the offending record with a cleared sequence_number.
//  4. Requeue every other sibling in processing state so they'll be re-signed
//     at fresh, post-reconcile sequence numbers. Without step 4, those
//     siblings would each hit the same mismatch when their submit fires.
func (s *Submitter) reconcileAndRequeue(ctx context.Context, rec *store.TransactionRecord, senderAddr aptossdk.AccountAddress) {
	s.logger.Warn("submitter: sequence mismatch, reconciling", "id", rec.ID, "sender", rec.SenderAddress)

	info, err := s.client.Inner.Account(senderAddr)
	if err != nil {
		s.logger.Error("submitter: fetch chain seq for reconcile", "id", rec.ID, "error", err)
		s.requeueTransient(ctx, rec, fmt.Errorf("reconcile account info: %w", err))
		return
	}
	chainSeq, err := info.SequenceNumber()
	if err != nil {
		s.logger.Error("submitter: parse chain seq for reconcile", "id", rec.ID, "error", err)
		s.requeueTransient(ctx, rec, fmt.Errorf("reconcile sequence number: %w", err))
		return
	}

	if err := s.queue.ReconcileSequence(ctx, rec.SenderAddress, chainSeq); err != nil {
		s.logger.Error("submitter: reconcile sequence", "id", rec.ID, "error", err)
	}

	// Re-queue all processing (claimed) records for this sender so they get
	// fresh sequence numbers after the reconcile.
	processing, lErr := s.queue.ListByStatus(ctx, store.StatusProcessing)
	if lErr == nil {
		for _, p := range processing {
			if p.SenderAddress == rec.SenderAddress && p.ID != rec.ID {
				s.requeueRecord(ctx, p)
			}
		}
	}

	hadSequence := rec.SequenceNumber != nil
	rec.Status = store.StatusQueued
	rec.SequenceNumber = nil
	rec.AttemptCount++
	rec.LastError = "sequence mismatch, re-queued after reconcile"
	rec.UpdatedAt = time.Now().UTC()
	if err := s.queue.Update(ctx, rec); err != nil {
		s.logger.Error("submitter: requeue after reconcile", "id", rec.ID, "error", err)
	}
	if hadSequence {
		if err := s.queue.ReleaseSequence(ctx, rec.SenderAddress); err != nil {
			s.logger.Error("submitter: release sequence after reconcile", "id", rec.ID, "error", err)
		}
	}
}

// drainPipeline is called after a submit failure cancels the pipeline. It
// requeues every signed-but-not-yet-submitted item sitting in the channel
// buffer — those transactions used sequence numbers that are now invalid (or
// at least suspect) and have to be re-signed from scratch.
//
// After draining, we query the node once to reconcile our counter with the
// chain's current sequence. This prevents the next worker invocation from
// repeating the same mistake if the failure came from a stale counter.
func (s *Submitter) drainPipeline(ctx context.Context, ch <-chan signedItem, senderAddress string) {
	for item := range ch {
		s.requeueRecord(ctx, item.rec)
	}
	senderAddr, err := aptos.ParseAddress(senderAddress)
	if err != nil {
		return
	}
	info, err := s.client.Inner.Account(senderAddr)
	if err != nil {
		s.logger.Error("submitter: drain fetch chain seq", "sender", senderAddress, "error", err)
		return
	}
	chainSeq, err := info.SequenceNumber()
	if err != nil {
		s.logger.Error("submitter: drain parse chain seq", "sender", senderAddress, "error", err)
		return
	}
	if err := s.queue.ReconcileSequence(ctx, senderAddress, chainSeq); err != nil {
		s.logger.Error("submitter: drain reconcile sequence", "sender", senderAddress, "error", err)
	}
}

// sweepOrphanedProcessing requeues any records for this sender that are still
// in "processing" after the worker exits. This handles the race where the
// producer commits a claim after pipeCancel but before it checks ctx.Err().
func (s *Submitter) sweepOrphanedProcessing(ctx context.Context, senderAddress string) {
	records, err := s.queue.ListByStatus(ctx, store.StatusProcessing)
	if err != nil {
		s.logger.Error("submitter: sweep orphaned", "sender", senderAddress, "error", err)
		return
	}
	for _, rec := range records {
		if rec.SenderAddress != senderAddress {
			continue
		}
		s.logger.Warn("submitter: sweeping orphaned processing record", "id", rec.ID, "sender", senderAddress)
		s.requeueRecord(ctx, rec)
	}
}

// isSequenceError classifies a submit error as "sequence number mismatch".
//
// This is string-based because the Aptos SDK surfaces these errors as plain
// errors without a machine-readable code. The triggers were chosen to match
// every wording the node is known to emit ("sequence_number too old", "invalid
// sequence number", etc.) while staying narrow enough not to catch unrelated
// errors. Fragile: any phrasing change on the node side will cause mismatches
// to fall through to the generic retry branch until max_retry_duration is hit.
func isSequenceError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sequence_number") ||
		strings.Contains(msg, "sequence number") ||
		strings.Contains(msg, "invalid_sequence")
}

// retrySleep backs off between retries in the producer loop with jitter to
// prevent thundering-herd when multiple workers recover from a shared
// downstream outage (e.g. Circle returning to service after a blip).
func (s *Submitter) retrySleep(ctx context.Context) {
	base := time.Duration(s.cfg.SubmitterRetryIntervalSeconds()) * time.Second
	jitter := time.Duration(rand.IntN(s.cfg.SubmitterRetryJitterSeconds()+1)) * time.Second
	select {
	case <-time.After(base + jitter):
	case <-ctx.Done():
	}
}

func resolveWallet(ctx context.Context, pkCache *circle.PublicKeyCache, rec *store.TransactionRecord) (config.CircleWallet, error) {
	pubKey, err := pkCache.Resolve(ctx, rec.WalletID)
	if err != nil {
		return config.CircleWallet{}, fmt.Errorf("resolve public key for wallet %s: %w", rec.WalletID, err)
	}
	return config.CircleWallet{
		WalletID:  rec.WalletID,
		Address:   rec.SenderAddress,
		PublicKey: pubKey,
	}, nil
}
