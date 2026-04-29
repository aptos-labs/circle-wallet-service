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
//
// The ctx variant is the one we actually call: it is wrapped with a per-call
// timeout (AptosSubmitTimeoutSeconds) so a hung node can't stall the signing
// pipeline past the stale-processing threshold.
type transactionSubmitter interface {
	SubmitTransactionCtx(ctx context.Context, signed *aptossdk.SignedTransaction) (*api.SubmitTransactionResponse, error)
}

// signedItem is the unit of work passed from the signing producer to the
// submitting consumer. It carries the original DB row (for status updates),
// the fully signed transaction ready to POST, the sequence number the signing
// pipeline used, and the transaction hash (computed once, during sign, so the
// submitter can pre-persist it before broadcast without re-serializing).
type signedItem struct {
	rec       *store.TransactionRecord
	signedTxn *aptossdk.SignedTransaction
	seqNum    uint64
	hash      string
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
	maxWorkers := s.cfg.SubmitterMaxActiveSenderWorkers()
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
				if len(workers) >= maxWorkers {
					s.logger.Warn("submitter: active sender worker cap reached",
						"active_workers", len(workers),
						"max_workers", maxWorkers,
					)
					break
				}
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
		// Log allocated sequence at claim time. The allocated sequence equals the
		// value of account_sequences.next_sequence BEFORE this claim's increment
		// — i.e. what the chain must accept for this submit to succeed.
		var claimedSeq uint64
		if rec.SequenceNumber != nil {
			claimedSeq = *rec.SequenceNumber
		}
		s.logger.Info("submitter: claimed",
			"id", rec.ID,
			"sender", senderAddress,
			"sequence", claimedSeq,
			"attempt", rec.AttemptCount,
		)

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
		s.markPermanentFailure(ctx, rec, "expired", "transaction expired before submit")
		return nil, false
	}

	maxDuration := time.Duration(s.cfg.SubmitterMaxRetryDurationSeconds()) * time.Second
	if time.Since(rec.CreatedAt) > maxDuration {
		s.markPermanentFailure(ctx, rec, "expired", "max retry duration exceeded")
		return nil, false
	}

	if rec.SequenceNumber == nil {
		s.requeueTransient(ctx, rec, fmt.Errorf("no sequence number allocated"))
		return nil, true
	}
	useSeq := *rec.SequenceNumber

	wallet, err := resolveWallet(ctx, s.pkCache, rec)
	if err != nil {
		s.markPermanentFailure(ctx, rec, "validation", err.Error())
		return nil, false
	}
	if err := wallet.VerifyWallet(); err != nil {
		s.markPermanentFailure(ctx, rec, "validation", "invalid wallet: "+err.Error())
		return nil, false
	}

	var qp store.QueuedPayload
	if err := json.Unmarshal([]byte(rec.PayloadJSON), &qp); err != nil {
		s.markPermanentFailure(ctx, rec, "validation", "bad payload_json: "+err.Error())
		return nil, false
	}

	entry, err := s.abi.BuildEntryFunctionPayload(rec.FunctionID, qp.TypeArguments, qp.Arguments)
	if err != nil {
		s.markPermanentFailure(ctx, rec, "validation", err.Error())
		return nil, false
	}

	senderAddr, err := aptos.ParseAddress(wallet.Address)
	if err != nil {
		s.markPermanentFailure(ctx, rec, "validation", err.Error())
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
			s.markPermanentFailure(ctx, rec, "validation", "invalid fee_payer_address: "+err.Error())
			return nil, false
		}
	} else {
		feePayerAddr = senderAddr
	}

	// Resolve the fee payer public key up front so we can feed it to
	// simulation before paying for Circle signing. For self-pay transactions
	// the sender key is reused.
	feePayerPubKey := wallet.PublicKey
	if hasSeparateFeePayer {
		fpPubKey, err := s.pkCache.Resolve(ctx, rec.FeePayerWalletID)
		if err != nil {
			s.requeueTransient(ctx, rec, fmt.Errorf("resolve fee-payer public key: %w", err))
			return nil, true
		}
		feePayerPubKey = fpPubKey
	}

	buildCtx, cancelBuild := withTimeout(ctx, s.cfg.AptosBuildTimeoutSeconds())
	rawTxn, err := s.client.BuildFeePayerTransactionCtx(buildCtx, senderAddr, feePayerAddr, payload, maxGas, useSeq)
	cancelBuild()
	if err != nil {
		s.requeueTransient(ctx, rec, err)
		return nil, true
	}

	// Simulate before we burn a Circle signing round-trip. On a VM-level
	// rejection (e.g. INSUFFICIENT_BALANCE) this record is terminated with
	// kind=simulation and ShiftSenderSequences unblocks every sibling behind
	// it. Transient node errors (503/429/network) fall through to the same
	// requeue path as a build failure — the simulate verdict is "unknown",
	// not "rejected", so we retry rather than fail.
	//
	// When gas calibration is enabled, the real max_gas_amount is tightened
	// to gas_used * 1.5 (or the caller's maxGas if they set something
	// smaller). This reduces the per-transaction gas reserve without risking
	// legitimate over-estimates from the VM.
	if s.cfg.SimulateBeforeSubmit() {
		simCtx, cancelSim := withTimeout(ctx, s.cfg.AptosSimulateTimeoutSeconds())
		userTxn, simErr := s.client.SimulateFeePayerTransaction(simCtx, rawTxn, wallet.PublicKey, feePayerPubKey)
		cancelSim()
		if simErr != nil {
			if aptos.IsTransientSimulationError(simErr) {
				s.requeueTransient(ctx, rec, fmt.Errorf("simulate: %w", simErr))
				return nil, true
			}
			s.markPermanentFailure(ctx, rec, "simulation", "simulation failed: "+simErr.Error())
			return nil, false
		}
		if !userTxn.Success {
			// A sequence VM status from simulation is NOT a real rejection —
			// the transaction would be valid at a different sequence number.
			// Route it through the same reconcile-and-requeue path the submit
			// failure uses so the local counter catches up to the chain and
			// the next claim gets a fresh, valid sequence. Treating it as a
			// permanent failure (as the default simulation path does) would
			// leave the counter stuck and every subsequent submit would hit
			// the same VM status.
			if aptos.IsSequenceVmStatus(userTxn.VmStatus) {
				s.logger.Warn("submitter: simulation reported sequence VM status; reconciling",
					"id", rec.ID,
					"sender", rec.SenderAddress,
					"sequence", useSeq,
					"vm_status", userTxn.VmStatus,
				)
				s.reconcileAndRequeue(ctx, rec, senderAddr)
				return nil, true
			}
			s.markSimulationFailure(ctx, rec, userTxn.VmStatus, "simulation rejected: "+userTxn.VmStatus)
			return nil, false
		}
		if s.cfg.CalibrateGasFromSimulation() && userTxn.GasUsed > 0 {
			calibrated := userTxn.GasUsed + userTxn.GasUsed/2 // gas_used * 1.5
			if maxGas == 0 || calibrated < maxGas {
				rebuilt, err := s.client.BuildFeePayerTransaction(senderAddr, feePayerAddr, payload, calibrated, useSeq)
				if err != nil {
					// Fall back to the original rawTxn; the pre-calibration
					// build already succeeded so a rebuild failure here is
					// surprising, but it's not worth failing the record over.
					s.logger.Warn("submitter: gas-calibration rebuild failed, using original", "id", rec.ID, "error", err)
				} else {
					rawTxn = rebuilt
				}
			}
		}
	}

	signCtx, cancelSign := withTimeout(ctx, s.cfg.CircleSignTimeoutSeconds())
	senderAuth, err := s.signer.SignTransaction(signCtx, rawTxn, wallet.WalletID, wallet.PublicKey)
	cancelSign()
	if err != nil {
		s.requeueTransient(ctx, rec, err)
		return nil, true
	}

	var feePayerAuth *crypto.AccountAuthenticator
	if hasSeparateFeePayer {
		fpSignCtx, cancelFpSign := withTimeout(ctx, s.cfg.CircleSignTimeoutSeconds())
		feePayerAuth, err = s.signer.SignTransaction(fpSignCtx, rawTxn, rec.FeePayerWalletID, feePayerPubKey)
		cancelFpSign()
		if err != nil {
			s.requeueTransient(ctx, rec, fmt.Errorf("fee-payer sign: %w", err))
			return nil, true
		}
	} else {
		feePayerAuth = senderAuth
	}

	signedTxn, ok := rawTxn.ToFeePayerSignedTransaction(senderAuth, feePayerAuth, []crypto.AccountAuthenticator{})
	if !ok {
		s.markPermanentFailure(ctx, rec, "assembly", "failed to assemble signed transaction")
		return nil, false
	}

	// Compute hash once, here, so submitSigned can pre-persist it without
	// re-serializing and without re-handling an error path mid-flight.
	hash, err := signedTxn.Hash()
	if err != nil {
		s.markPermanentFailure(ctx, rec, "assembly", "compute txn hash: "+err.Error())
		return nil, false
	}

	return &signedItem{rec: rec, signedTxn: signedTxn, seqNum: useSeq, hash: hash}, false
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
//
// Duplicate-submit safety. The signed transaction's hash is computed and
// persisted to the DB BEFORE broadcast. This is the linchpin: if the chain
// accepts the transaction but the post-submit status update fails, the hash
// is already durably associated with the record. The poller's processing+hash
// recovery path (see internal/poller) will confirm by on-chain lookup without
// any re-sign — a re-sign would allocate a different sequence number and
// produce a duplicate broadcast for the same logical request. Sweepers
// (RecoverStaleProcessing, sweepOrphanedProcessing) skip rows with txn_hash
// set so they won't revert a pre-broadcast hash back to queued.
func (s *Submitter) submitSigned(ctx context.Context, item *signedItem) bool {
	// Pre-persist the hash before broadcast. The hash was computed during
	// signing (see prepareRecord) and is now durably written so that any
	// post-submit failure can be recovered by the poller without re-signing.
	//
	// The persist is conditional on status=processing. If a slow Circle
	// signing round-trip lets RecoverStaleProcessing fire against this row
	// first, the row has already been flipped back to queued and the
	// counter decremented — broadcasting our signed transaction now would
	// burn a sequence the next claim will reuse, producing a duplicate
	// on-chain submission. When UpdateIfStatus reports no-op we bail out
	// cleanly: the row belongs to the recovery path and a later dispatcher
	// tick will re-claim it at a fresh sequence.
	item.rec.TxnHash = item.hash
	item.rec.UpdatedAt = time.Now().UTC()
	updated, err := s.queue.UpdateIfStatus(ctx, item.rec, store.StatusProcessing)
	if err != nil {
		// Nothing has hit the chain yet; clear the in-memory hash and
		// requeue transiently so the next attempt starts clean.
		s.logger.Error("submitter: pre-submit hash persist failed",
			"id", item.rec.ID, "hash", item.hash, "error", err)
		item.rec.TxnHash = ""
		s.requeueTransient(ctx, item.rec, fmt.Errorf("persist pre-submit hash: %w", err))
		return false
	}
	if !updated {
		// Row is no longer in processing — recover/sweep won the race while
		// we were signing. Counter was already decremented by the winner;
		// do NOT ReleaseSequence here (that would decrement twice) and do
		// NOT broadcast. The row sits in queued with sequence_number=NULL
		// and will be re-claimed on the next tick.
		s.logger.Warn("submitter: pre-submit hash persist skipped; row no longer in processing",
			"id", item.rec.ID,
			"hash", item.hash,
			"sequence", item.seqNum,
		)
		item.rec.TxnHash = ""
		return false
	}

	sub := s.txSubmit
	if sub == nil {
		sub = s.client
	}
	// Pre-submit log: the sequence number being sent to the chain. Paired with
	// the claim log above, this bounds exactly which counter value was used for
	// this submit attempt.
	s.logger.Info("submitter: submitting",
		"id", item.rec.ID,
		"sender", item.rec.SenderAddress,
		"sequence", item.seqNum,
		"hash", item.hash,
		"attempt", item.rec.AttemptCount,
	)
	submitCtx, cancelSubmit := withTimeout(ctx, s.cfg.AptosSubmitTimeoutSeconds())
	submitResp, err := sub.SubmitTransactionCtx(submitCtx, item.signedTxn)
	cancelSubmit()
	if err != nil {
		// Submit failed: nothing on chain. Clear the pre-persisted hash so
		// downstream failure paths don't leave a stale hash on the row.
		item.rec.TxnHash = ""
		if isSequenceError(err) {
			s.logger.Warn("submitter: submit rejected as sequence error",
				"id", item.rec.ID,
				"sender", item.rec.SenderAddress,
				"sequence", item.seqNum,
				"error", err,
			)
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
			s.markPermanentFailure(ctx, item.rec, "submit", err.Error())
		} else {
			s.requeueTransient(ctx, item.rec, err)
		}
		return false
	}

	// Defensive: server-returned hash should match what we computed.
	if submitResp.Hash != "" && submitResp.Hash != item.hash {
		s.logger.Warn("submitter: server-returned hash differs from local",
			"id", item.rec.ID, "local", item.hash, "server", submitResp.Hash)
		item.rec.TxnHash = submitResp.Hash
	}

	item.rec.Status = store.StatusSubmitted
	item.rec.UpdatedAt = time.Now().UTC()
	item.rec.LastError = ""
	if err := s.updateWithRetry(ctx, item.rec); err != nil {
		// The chain has accepted the transaction (hash is persisted) but we
		// can't flip the status. Leave the record in processing+hash state;
		// the poller's recovery path will confirm it by on-chain lookup.
		// Do NOT requeue: that would clear the sequence and risk a duplicate
		// re-sign at a new sequence number.
		s.logger.Error("submitter: post-submit status update failed; record left in processing+hash for poller recovery",
			"id", item.rec.ID,
			"hash", item.rec.TxnHash,
			"sequence", item.seqNum,
			"error", err,
		)
		return false
	}

	s.logger.Info("submitter: submitted", "id", item.rec.ID, "hash", item.rec.TxnHash, "sequence", item.seqNum)
	return true
}

// updateWithRetry retries queue.Update with a short bounded exponential backoff.
// Used on the post-submit status transition, where giving up and requeuing
// would cause a duplicate broadcast at a new sequence number. Five attempts
// across ~1.5 seconds covers routine DB blips; beyond that the poller's
// processing+hash recovery path takes over.
func (s *Submitter) updateWithRetry(ctx context.Context, rec *store.TransactionRecord) error {
	const maxAttempts = 5
	var lastErr error
	for i := range maxAttempts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.queue.Update(ctx, rec); err == nil {
			return nil
		} else {
			lastErr = err
		}
		backoff := time.Duration(50*(1<<i)) * time.Millisecond // 50, 100, 200, 400, 800ms
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return lastErr
}

// markPermanentFailure terminates a transaction with status=failed and fires
// a webhook. Because this record held a sequence number that will never be
// used on chain, ShiftSenderSequences slides every higher-numbered sibling
// back to queued so they'll be re-claimed with fresh sequences — this
// prevents a permanent gap in the per-sender sequence stream that would block
// every subsequent submit for that account.
//
// kind categorizes the failure for consumers (e.g. "simulation", "submit",
// "expired", "validation"). It is persisted on the row and included in the
// webhook payload so handlers don't have to parse msg to route failures.
func (s *Submitter) markPermanentFailure(ctx context.Context, rec *store.TransactionRecord, kind, msg string) {
	var failedSeq uint64
	hasSeq := rec.SequenceNumber != nil
	if hasSeq {
		failedSeq = *rec.SequenceNumber
	}
	s.logger.Warn("submitter: permanent failure",
		"id", rec.ID,
		"sender", rec.SenderAddress,
		"kind", kind,
		"sequence", failedSeq,
		"has_sequence", hasSeq,
		"msg", msg,
	)
	rec.Status = store.StatusFailed
	rec.ErrorMessage = msg
	rec.FailureKind = kind
	rec.UpdatedAt = time.Now().UTC()
	if err := s.queue.Update(ctx, rec); err != nil {
		s.logger.Error("submitter: mark failed", "id", rec.ID, "error", err)
	}
	if hasSeq {
		if err := s.queue.ShiftSenderSequences(ctx, rec.SenderAddress, failedSeq); err != nil {
			s.logger.Error("submitter: shift sequences", "sender", rec.SenderAddress, "error", err)
		} else {
			s.logger.Info("submitter: shifted siblings after permanent failure",
				"id", rec.ID,
				"sender", rec.SenderAddress,
				"failed_seq", failedSeq,
			)
		}
	}
	s.notifier.Notify(ctx, rec)
}

// markSimulationFailure is the simulation-specific variant that additionally
// records the Aptos VM's structured failure reason. Consumers can use
// vm_status to distinguish state-dependent failures (INSUFFICIENT_BALANCE) from
// code-level bugs (MISSING_DATA, OUT_OF_GAS) without parsing error_message.
func (s *Submitter) markSimulationFailure(ctx context.Context, rec *store.TransactionRecord, vmStatus, msg string) {
	rec.VmStatus = vmStatus
	s.markPermanentFailure(ctx, rec, "simulation", msg)
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
	var releasedSeq uint64
	hadSequence := rec.SequenceNumber != nil
	if hadSequence {
		releasedSeq = *rec.SequenceNumber
	}
	s.logger.Warn("submitter: retry",
		"id", rec.ID,
		"sender", rec.SenderAddress,
		"had_sequence", hadSequence,
		"released_seq", releasedSeq,
		"error", err,
	)
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
		} else {
			s.logger.Info("submitter: released sequence (counter -1)",
				"id", rec.ID,
				"sender", rec.SenderAddress,
				"released_seq", releasedSeq,
			)
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
	var localSeq uint64
	if rec.SequenceNumber != nil {
		localSeq = *rec.SequenceNumber
	}
	s.logger.Warn("submitter: sequence mismatch, reconciling",
		"id", rec.ID,
		"sender", rec.SenderAddress,
		"local_seq_used", localSeq,
	)

	acctCtx, cancelAcct := withTimeout(ctx, s.cfg.AptosAccountLookupTimeoutSeconds())
	info, err := s.client.AccountCtx(acctCtx, senderAddr)
	cancelAcct()
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

	s.applyReconcile(ctx, rec, localSeq, chainSeq)
}

// applyReconcile is the post-fetch half of reconcileAndRequeue. Split out so
// tests can exercise the counter-mutation logic without needing a live Aptos
// node.
//
// Counter-semantics invariant: we must NOT call ReleaseSequence (or
// ShiftSenderSequences) for any record whose sequence we've given up here.
// ReconcileSequence just snapped the counter to chain truth; decrementing it
// per released record would push the counter back into the "too old" zone
// and we'd loop forever at drift=N. The slots being "released" were already
// invalidated when reconcile bumped the counter — decrementing would
// double-count the correction. Post-reconcile, the counter is authoritative;
// any stale allocations silently evaporate.
func (s *Submitter) applyReconcile(ctx context.Context, rec *store.TransactionRecord, localSeq, chainSeq uint64) {
	// Direct comparison: the chain's current sequence vs what we just tried to
	// submit. If chain_seq > local_seq_used, our local counter was behind —
	// most commonly because the account had pre-existing txns when this
	// service first touched it and the DB counter started at 0.
	s.logger.Info("submitter: reconcile chain state",
		"id", rec.ID,
		"sender", rec.SenderAddress,
		"local_seq_used", localSeq,
		"chain_seq", chainSeq,
		"drift", int64(chainSeq)-int64(localSeq),
	)

	// When the chain is BEHIND what we just tried (drift < 0 — most commonly
	// caused by a run of SEQUENCE_NUMBER_TOO_NEW simulations burning slots
	// without landing) GREATEST is a no-op and every retry allocates a
	// further-ahead sequence. ForceResetSequenceToChain snaps the counter
	// down to chainSeq + in-flight-count so the next claim reuses a valid
	// slot. When the chain is at or ahead of us the original one-way-up
	// ReconcileSequence keeps counter monotonicity safe.
	if chainSeq < localSeq {
		if err := s.queue.ForceResetSequenceToChain(ctx, rec.SenderAddress, chainSeq); err != nil {
			s.logger.Error("submitter: force reset sequence", "id", rec.ID, "error", err)
		} else {
			s.logger.Info("submitter: reset counter (chain-behind)",
				"sender", rec.SenderAddress,
				"chain_seq", chainSeq,
				"local_seq_used", localSeq,
			)
		}
	} else {
		if err := s.queue.ReconcileSequence(ctx, rec.SenderAddress, chainSeq); err != nil {
			s.logger.Error("submitter: reconcile sequence", "id", rec.ID, "error", err)
		} else {
			s.logger.Info("submitter: reconciled counter",
				"sender", rec.SenderAddress,
				"counter_raised_to_at_least", chainSeq,
			)
		}
	}

	// Re-queue all processing (claimed) records for this sender so they get
	// fresh sequence numbers after the reconcile. See the invariant comment
	// above for why requeueWithoutRelease is load-bearing here.
	//
	// Rows with txn_hash set are deliberately skipped. A processing row with
	// a hash means the submitter already pre-persisted and broadcast the
	// signed transaction to chain — clearing the sequence and re-queueing
	// would cause the next worker to re-sign at a fresh sequence, producing
	// a second on-chain broadcast for the same logical request. The poller's
	// processing+hash recovery path owns those rows; reconcile must keep its
	// hands off them for the same reason sweepOrphanedProcessing does.
	processing, lErr := s.queue.ListByStatus(ctx, store.StatusProcessing)
	if lErr == nil {
		for _, p := range processing {
			if p.SenderAddress != rec.SenderAddress || p.ID == rec.ID {
				continue
			}
			if p.TxnHash != "" {
				// Already broadcast; poller confirms by hash.
				continue
			}
			s.requeueWithoutRelease(ctx, p)
		}
	}

	rec.Status = store.StatusQueued
	rec.SequenceNumber = nil
	rec.AttemptCount++
	rec.LastError = "sequence mismatch, re-queued after reconcile"
	rec.UpdatedAt = time.Now().UTC()
	if err := s.queue.Update(ctx, rec); err != nil {
		s.logger.Error("submitter: requeue after reconcile", "id", rec.ID, "error", err)
	}
}

// requeueWithoutRelease is the reconcile-path variant of requeueRecord: it
// clears sequence_number and flips the row back to queued, but does NOT
// decrement the sender's sequence counter. See the comment in
// reconcileAndRequeue for why releasing after a reconcile is incorrect.
func (s *Submitter) requeueWithoutRelease(ctx context.Context, rec *store.TransactionRecord) {
	rec.Status = store.StatusQueued
	rec.SequenceNumber = nil
	rec.UpdatedAt = time.Now().UTC()
	if err := s.queue.Update(ctx, rec); err != nil {
		s.logger.Error("submitter: requeue without release", "id", rec.ID, "error", err)
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
	acctCtx, cancelAcct := withTimeout(ctx, s.cfg.AptosAccountLookupTimeoutSeconds())
	info, err := s.client.AccountCtx(acctCtx, senderAddr)
	cancelAcct()
	if err != nil {
		s.logger.Error("submitter: drain fetch chain seq", "sender", senderAddress, "error", err)
		return
	}
	chainSeq, err := info.SequenceNumber()
	if err != nil {
		s.logger.Error("submitter: drain parse chain seq", "sender", senderAddress, "error", err)
		return
	}
	s.logger.Info("submitter: drain reconcile", "sender", senderAddress, "chain_seq", chainSeq)
	if err := s.queue.ReconcileSequence(ctx, senderAddress, chainSeq); err != nil {
		s.logger.Error("submitter: drain reconcile sequence", "sender", senderAddress, "error", err)
	}
}

// sweepOrphanedProcessing requeues any records for this sender that are still
// in "processing" after the worker exits. This handles the race where the
// producer commits a claim after pipeCancel but before it checks ctx.Err().
//
// Records with txn_hash set are deliberately skipped: they were broadcast to
// chain but their post-submit status update failed. Requeuing them would
// clear the sequence and trigger a re-sign at a different sequence number,
// producing a duplicate on-chain. The poller's processing+hash recovery path
// owns these rows.
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
		if rec.TxnHash != "" {
			// Already broadcast; poller confirms by hash.
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

// withTimeout returns a child context with the given per-call deadline, or
// a cancellable copy of the parent when seconds <= 0 (timeout disabled).
// The returned cancel func must always be called by the caller so that
// resources are released even when the child ctx completes normally.
//
// This wraps every external RPC the submitter issues (Circle signing, Aptos
// build/simulate/submit/account lookup) so that one unresponsive remote can't
// pin a per-sender worker past SubmitterStaleProcessingSeconds. If that
// happened, RecoverStaleProcessing would start racing the in-flight pipeline
// for the same row and the duplicate-submit guards in submitSigned would be
// the only thing between a slow node and a double broadcast.
func withTimeout(parent context.Context, seconds int) (context.Context, context.CancelFunc) {
	if seconds <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, time.Duration(seconds)*time.Second)
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
