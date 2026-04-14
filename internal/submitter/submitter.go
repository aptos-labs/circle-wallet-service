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

type Notifier interface {
	Notify(rec *store.TransactionRecord)
}

type transactionSubmitter interface {
	SubmitTransaction(signed *aptossdk.SignedTransaction) (*api.SubmitTransactionResponse, error)
}

type signedItem struct {
	rec       *store.TransactionRecord
	signedTxn *aptossdk.SignedTransaction
	seqNum    uint64
}

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
		cfg:    cfg,
		queue:  q,
		client: client,
		abi:    abi,
		signer:   signer,
		pkCache:  pkCache,
		notifier: notifier,
		logger:   logger,
	}
}

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

func (s *Submitter) runSenderWorker(ctx context.Context, senderAddress string) {
	depth := s.cfg.SubmitterSigningPipelineDepth()
	if depth < 1 {
		depth = 1
	}

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
}

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

func (s *Submitter) markPermanentFailure(ctx context.Context, rec *store.TransactionRecord, msg string) {
	rec.Status = store.StatusFailed
	rec.ErrorMessage = msg
	rec.UpdatedAt = time.Now().UTC()
	if err := s.queue.Update(ctx, rec); err != nil {
		s.logger.Error("submitter: mark failed", "id", rec.ID, "error", err)
	}
	s.notifier.Notify(rec)
	if rec.SequenceNumber != nil {
		if err := s.queue.ShiftSenderSequences(ctx, rec.SenderAddress, *rec.SequenceNumber); err != nil {
			s.logger.Error("submitter: shift sequences", "sender", rec.SenderAddress, "error", err)
		}
	}
}

func (s *Submitter) requeueTransient(ctx context.Context, rec *store.TransactionRecord, err error) {
	s.logger.Warn("submitter: retry", "id", rec.ID, "error", err)
	rec.Status = store.StatusQueued
	rec.SequenceNumber = nil
	rec.AttemptCount++
	rec.LastError = err.Error()
	rec.UpdatedAt = time.Now().UTC()
	if err2 := s.queue.Update(ctx, rec); err2 != nil {
		s.logger.Error("submitter: requeue", "id", rec.ID, "error", err2)
	}
}

func (s *Submitter) requeueRecord(ctx context.Context, rec *store.TransactionRecord) {
	rec.Status = store.StatusQueued
	rec.SequenceNumber = nil
	rec.UpdatedAt = time.Now().UTC()
	if err := s.queue.Update(ctx, rec); err != nil {
		s.logger.Error("submitter: requeue on cancel", "id", rec.ID, "error", err)
	}
}

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

	if err := s.queue.UpsertNextSequence(ctx, rec.SenderAddress, chainSeq); err != nil {
		s.logger.Error("submitter: reconcile sequence", "id", rec.ID, "error", err)
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
	if err := s.queue.UpsertNextSequence(ctx, senderAddress, chainSeq); err != nil {
		s.logger.Error("submitter: drain reset sequence", "sender", senderAddress, "error", err)
	}
}

func isSequenceError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SEQUENCE_NUMBER") || strings.Contains(msg, "sequence_number")
}

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
