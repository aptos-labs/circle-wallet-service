package txn

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"

	"github.com/aptos-labs/jc-contract-integration/internal/account"
	"github.com/aptos-labs/jc-contract-integration/internal/signer"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

// Manager orchestrates the transaction lifecycle: build → sign → submit → track.
type Manager struct {
	client          TxnClient
	store           store.Store
	registry        *account.Registry
	maxRetry        int
	expirationSec   int
	maxGasAmount    uint64
	gasPerRecipient uint64
	logger          *slog.Logger
}

// NewManager creates a new transaction manager.
func NewManager(
	client TxnClient,
	st store.Store,
	registry *account.Registry,
	maxRetry int,
	expirationSec int,
	maxGasAmount uint64,
	gasPerRecipient uint64,
	logger *slog.Logger,
) *Manager {
	return &Manager{
		client:          client,
		store:           st,
		registry:        registry,
		maxRetry:        maxRetry,
		expirationSec:   expirationSec,
		maxGasAmount:    maxGasAmount,
		gasPerRecipient: gasPerRecipient,
		logger:          logger,
	}
}

// Submit executes the full transaction lifecycle for an operation and returns a tracking ID.
func (m *Manager) Submit(ctx context.Context, op Operation) (string, error) {
	// 1. Resolve role → signer
	s, err := m.registry.Get(op.RequiredRole())
	if err != nil {
		return "", fmt.Errorf("resolve signer: %w", err)
	}

	// 2. Build payload
	payload, err := op.BuildPayload()
	if err != nil {
		return "", fmt.Errorf("build payload: %w", err)
	}

	// 3. Build orderless transaction with random nonce
	senderAddr := s.Address()
	gasAmount := m.computeGas(op)
	rawTxn, nonce, err := m.client.BuildOrderlessTransaction(senderAddr, payload, gasAmount)
	if err != nil {
		return "", fmt.Errorf("build transaction: %w", err)
	}

	// 4. Create tracking record
	txnID := uuid.New().String()
	rec := &store.TransactionRecord{
		ID:             txnID,
		OperationType:  op.Name(),
		Status:         store.StatusPending,
		Nonce:          nonce,
		SenderAddress:  senderAddr.String(),
		Attempt:        0,
		MaxRetries:     m.maxRetry,
		RequestPayload: string(op.RequestJSON()),
		ExpiresAt:      time.Now().UTC().Add(time.Duration(m.expirationSec) * time.Second),
	}

	if err := m.store.CreateTransaction(ctx, rec); err != nil {
		return "", fmt.Errorf("persist pending: %w", err)
	}

	// 5. Sign and build signed transaction.
	// TransactionSigner (e.g. CircleSigner) handles fee-payer wrapping internally;
	// plain Signer uses the standard signing-message flow.
	var signedTxn *aptossdk.SignedTransaction
	if ts, ok := s.(signer.TransactionSigner); ok {
		signedTxn, err = ts.SignTransaction(ctx, rawTxn)
		if err != nil {
			m.markFailed(ctx, rec, fmt.Sprintf("sign transaction: %v", err))
			return txnID, nil
		}
	} else {
		signingMsg, err := rawTxn.SigningMessage()
		if err != nil {
			m.markFailed(ctx, rec, fmt.Sprintf("signing message: %v", err))
			return txnID, nil
		}

		auth, err := s.Sign(ctx, signingMsg)
		if err != nil {
			m.markFailed(ctx, rec, fmt.Sprintf("sign: %v", err))
			return txnID, nil
		}

		signedTxn, err = rawTxn.SignedTransactionWithAuthenticator(auth)
		if err != nil {
			m.markFailed(ctx, rec, fmt.Sprintf("build signed txn: %v", err))
			return txnID, nil
		}
	}

	submitResp, err := m.client.SubmitTransaction(signedTxn)
	if err != nil {
		m.markFailed(ctx, rec, fmt.Sprintf("submit: %v", err))
		return txnID, nil
	}

	// 7. Update to submitted
	rec.Status = store.StatusSubmitted
	rec.TxnHash = submitResp.Hash
	if err := m.store.UpdateTransaction(ctx, rec); err != nil {
		m.logger.Error("failed to update submitted status", "txn_id", txnID, "error", err)
	}

	m.logger.Info("transaction submitted",
		"txn_id", txnID,
		"txn_hash", submitResp.Hash,
		"operation", op.Name(),
		"nonce", nonce,
	)

	return txnID, nil
}

// GetTransaction retrieves a tracked transaction by ID.
func (m *Manager) GetTransaction(ctx context.Context, id string) (*store.TransactionRecord, error) {
	return m.store.GetTransaction(ctx, id)
}

// computeGas returns the gas amount for an operation, scaling by recipient count if applicable.
func (m *Manager) computeGas(op Operation) uint64 {
	gas := m.maxGasAmount
	if m.gasPerRecipient > 0 {
		if rc, ok := op.(RecipientCounter); ok {
			gas += uint64(rc.RecipientCount()) * m.gasPerRecipient
		}
	}
	return gas
}

// Resubmit rebuilds and submits an existing transaction record (for stuck transaction recovery).
func (m *Manager) Resubmit(ctx context.Context, rec *store.TransactionRecord, op Operation) error {
	s, err := m.registry.Get(op.RequiredRole())
	if err != nil {
		return fmt.Errorf("resolve signer: %w", err)
	}

	payload, err := op.BuildPayload()
	if err != nil {
		return fmt.Errorf("build payload: %w", err)
	}

	senderAddr := s.Address()
	gasAmount := m.computeGas(op)
	rawTxn, nonce, err := m.client.BuildOrderlessTransaction(senderAddr, payload, gasAmount)
	if err != nil {
		return fmt.Errorf("build transaction: %w", err)
	}

	var signedTxn *aptossdk.SignedTransaction
	if ts, ok := s.(signer.TransactionSigner); ok {
		signedTxn, err = ts.SignTransaction(ctx, rawTxn)
		if err != nil {
			return fmt.Errorf("sign transaction: %w", err)
		}
	} else {
		signingMsg, err := rawTxn.SigningMessage()
		if err != nil {
			return fmt.Errorf("signing message: %w", err)
		}

		auth, err := s.Sign(ctx, signingMsg)
		if err != nil {
			return fmt.Errorf("sign: %w", err)
		}

		signedTxn, err = rawTxn.SignedTransactionWithAuthenticator(auth)
		if err != nil {
			return fmt.Errorf("build signed txn: %w", err)
		}
	}

	submitResp, err := m.client.SubmitTransaction(signedTxn)
	if err != nil {
		return fmt.Errorf("submit: %w", err)
	}

	rec.Status = store.StatusSubmitted
	rec.TxnHash = submitResp.Hash
	rec.Nonce = nonce
	rec.ExpiresAt = time.Now().UTC().Add(time.Duration(m.expirationSec) * time.Second)
	if err := m.store.UpdateTransaction(ctx, rec); err != nil {
		m.logger.Error("failed to update resubmitted status", "txn_id", rec.ID, "error", err)
	}

	m.logger.Info("transaction resubmitted",
		"txn_id", rec.ID,
		"txn_hash", submitResp.Hash,
		"operation", rec.OperationType,
		"attempt", rec.Attempt,
	)
	return nil
}

func (m *Manager) markFailed(ctx context.Context, rec *store.TransactionRecord, errMsg string) {
	rec.Status = store.StatusFailed
	rec.ErrorMessage = errMsg
	if err := m.store.UpdateTransaction(ctx, rec); err != nil {
		m.logger.Error("failed to update failed status",
			"txn_id", rec.ID,
			"original_error", errMsg,
			"update_error", err,
		)
	}
	m.logger.Warn("transaction failed", "txn_id", rec.ID, "error", errMsg)
}
