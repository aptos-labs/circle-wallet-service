package submitter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
	"github.com/aptos-labs/jc-contract-integration/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/internal/circle"
	"github.com/aptos-labs/jc-contract-integration/internal/config"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

const maxSubmitAttempts = 32

// Submitter processes queued transactions: reconciles sequence numbers with the chain,
// builds fee-payer transactions, signs via Circle, and submits to Aptos.
type Submitter struct {
	cfg    *config.Config
	queue  store.Queue
	client *aptos.Client
	abi    *aptos.ABICache
	signer *circle.Signer
	logger *slog.Logger
}

func New(
	cfg *config.Config,
	q store.Queue,
	client *aptos.Client,
	abi *aptos.ABICache,
	signer *circle.Signer,
	logger *slog.Logger,
) *Submitter {
	return &Submitter{
		cfg:    cfg,
		queue:  q,
		client: client,
		abi:    abi,
		signer: signer,
		logger: logger,
	}
}

// Run polls the queue until ctx is cancelled.
func (s *Submitter) Run(ctx context.Context) {
	go s.recoverLoop(ctx)

	ticker := time.NewTicker(350 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processOnce(ctx)
		}
	}
}

func (s *Submitter) recoverLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.queue.RecoverStaleProcessing(ctx, 2*time.Minute)
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

func (s *Submitter) processOnce(ctx context.Context) {
	rec, dbNext, err := s.queue.ClaimNextQueued(ctx)
	if err != nil {
		s.logger.Error("submitter: claim", "error", err)
		return
	}
	if rec == nil {
		return
	}

	now := time.Now().UTC()
	if now.After(rec.ExpiresAt) {
		rec.Status = store.StatusExpired
		rec.ErrorMessage = "transaction expired before submit"
		rec.UpdatedAt = now
		if err := s.queue.Update(ctx, rec); err != nil {
			s.logger.Error("submitter: mark expired", "id", rec.ID, "error", err)
		}
		return
	}

	if rec.AttemptCount >= maxSubmitAttempts {
		rec.Status = store.StatusFailed
		rec.ErrorMessage = "max submit attempts exceeded"
		rec.UpdatedAt = now
		if err := s.queue.Update(ctx, rec); err != nil {
			s.logger.Error("submitter: mark failed max attempts", "id", rec.ID, "error", err)
		}
		return
	}

	wallet, err := resolveWallet(s.cfg, rec)
	if err != nil {
		rec.Status = store.StatusFailed
		rec.ErrorMessage = err.Error()
		rec.UpdatedAt = now
		_ = s.queue.Update(ctx, rec)
		return
	}
	if err := wallet.VerifyWallet(); err != nil {
		rec.Status = store.StatusFailed
		rec.ErrorMessage = "invalid wallet: " + err.Error()
		rec.UpdatedAt = now
		_ = s.queue.Update(ctx, rec)
		return
	}

	var qp store.QueuedPayload
	if err := json.Unmarshal([]byte(rec.PayloadJSON), &qp); err != nil {
		rec.Status = store.StatusFailed
		rec.ErrorMessage = "bad payload_json: " + err.Error()
		rec.UpdatedAt = now
		_ = s.queue.Update(ctx, rec)
		return
	}

	entry, err := s.abi.BuildEntryFunctionPayload(rec.FunctionID, qp.TypeArguments, qp.Arguments)
	if err != nil {
		rec.Status = store.StatusFailed
		rec.ErrorMessage = err.Error()
		rec.UpdatedAt = now
		_ = s.queue.Update(ctx, rec)
		return
	}

	senderAddr, err := aptos.ParseAddress(wallet.Address)
	if err != nil {
		rec.Status = store.StatusFailed
		rec.ErrorMessage = err.Error()
		rec.UpdatedAt = now
		_ = s.queue.Update(ctx, rec)
		return
	}

	info, err := s.client.Inner.Account(senderAddr)
	if err != nil {
		s.failOrRequeue(ctx, rec, fmt.Errorf("account info: %w", err))
		return
	}
	chainSeq, err := info.SequenceNumber()
	if err != nil {
		s.failOrRequeue(ctx, rec, fmt.Errorf("sequence number: %w", err))
		return
	}

	useSeq := dbNext
	if chainSeq > useSeq {
		useSeq = chainSeq
	}

	var maxGas uint64
	if rec.MaxGasAmount != nil {
		maxGas = *rec.MaxGasAmount
	}

	payload := aptossdk.TransactionPayload{Payload: entry}
	rawTxn, err := s.client.BuildFeePayerTransaction(senderAddr, senderAddr, payload, maxGas, useSeq)
	if err != nil {
		s.failOrRequeue(ctx, rec, err)
		return
	}

	auth, err := s.signer.SignTransaction(ctx, rawTxn, wallet.WalletID, wallet.PublicKey)
	if err != nil {
		s.failOrRequeue(ctx, rec, err)
		return
	}

	signedTxn, ok := rawTxn.ToFeePayerSignedTransaction(auth, auth, []crypto.AccountAuthenticator{})
	if !ok {
		rec.Status = store.StatusFailed
		rec.ErrorMessage = "failed to assemble signed transaction"
		rec.UpdatedAt = time.Now().UTC()
		_ = s.queue.Update(ctx, rec)
		return
	}

	submitResp, err := s.client.SubmitTransaction(signedTxn)
	if err != nil {
		s.failOrRequeue(ctx, rec, err)
		return
	}

	seq := useSeq
	rec.Status = store.StatusSubmitted
	rec.TxnHash = submitResp.Hash
	rec.SequenceNumber = &seq
	rec.UpdatedAt = time.Now().UTC()
	rec.LastError = ""
	if err := s.queue.Update(ctx, rec); err != nil {
		s.logger.Error("submitter: update submitted", "id", rec.ID, "error", err)
		return
	}

	if err := s.queue.UpsertNextSequence(ctx, rec.SenderAddress, useSeq+1); err != nil {
		s.logger.Error("submitter: upsert sequence", "sender", rec.SenderAddress, "error", err)
	}

	s.logger.Info("submitter: submitted", "id", rec.ID, "hash", rec.TxnHash, "sequence", useSeq)
}

func (s *Submitter) failOrRequeue(ctx context.Context, rec *store.TransactionRecord, err error) {
	s.logger.Warn("submitter: retry", "id", rec.ID, "error", err)
	rec.Status = store.StatusQueued
	rec.AttemptCount++
	rec.LastError = err.Error()
	rec.UpdatedAt = time.Now().UTC()
	if err2 := s.queue.Update(ctx, rec); err2 != nil {
		s.logger.Error("submitter: requeue", "id", rec.ID, "error", err2)
	}
}

func resolveWallet(cfg *config.Config, rec *store.TransactionRecord) (config.CircleWallet, error) {
	var qp store.QueuedPayload
	if err := json.Unmarshal([]byte(rec.PayloadJSON), &qp); err != nil {
		return config.CircleWallet{}, err
	}
	if qp.Wallet != nil && qp.Wallet.WalletID != "" && qp.Wallet.Address != "" && qp.Wallet.PublicKey != "" {
		return config.CircleWallet{
			WalletID:  qp.Wallet.WalletID,
			Address:   qp.Wallet.Address,
			PublicKey: qp.Wallet.PublicKey,
		}, nil
	}
	w, ok := cfg.LookupWallet(rec.WalletID)
	if !ok {
		return config.CircleWallet{}, fmt.Errorf("unknown wallet_id %q", rec.WalletID)
	}
	return w, nil
}
