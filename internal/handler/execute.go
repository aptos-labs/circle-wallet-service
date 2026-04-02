package handler

import (
	"log/slog"
	"net/http"
	"time"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
	"github.com/aptos-labs/jc-contract-integration/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/internal/circle"
	"github.com/aptos-labs/jc-contract-integration/internal/config"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/google/uuid"
)

type executeRequest struct {
	WalletID      string   `json:"wallet_id"`
	FunctionID    string   `json:"function_id"`
	TypeArguments []string `json:"type_arguments"`
	Arguments     []any    `json:"arguments"`
	MaxGasAmount  *uint64  `json:"max_gas_amount,omitempty"`
	WebhookURL    string   `json:"webhook_url,omitempty"`
}

// Execute handles POST /v1/execute.
func Execute(
	cfg *config.Config,
	client *aptos.Client,
	abiCache *aptos.ABICache,
	signer *circle.Signer,
	st store.Store,
	logger *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req executeRequest
		if err := decodeJSON(r, &req); err != nil {
			errorResponse(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if req.WalletID == "" {
			errorResponse(w, http.StatusBadRequest, "wallet_id is required")
			return
		}
		if req.FunctionID == "" {
			errorResponse(w, http.StatusBadRequest, "function_id is required")
			return
		}

		// Look up wallet by wallet_id or address.
		wallet, ok := cfg.LookupWallet(req.WalletID)
		if !ok {
			errorResponse(w, http.StatusBadRequest, "unknown wallet_id or address")
			return
		}

		// Build payload
		entryFunction, err := abiCache.BuildEntryFunctionPayload(req.FunctionID, req.TypeArguments, req.Arguments)
		if err != nil {
			errorResponse(w, http.StatusInternalServerError, "failed to build entry function payload: "+err.Error())
			return
		}

		payload := aptossdk.TransactionPayload{
			Payload: entryFunction,
		}

		// Parse sender address from wallet.
		senderAddr, err := aptos.ParseAddress(wallet.Address)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "invalid wallet address: "+err.Error())
			return
		}

		// Resolve gas override.
		var maxGas uint64
		if req.MaxGasAmount != nil {
			maxGas = *req.MaxGasAmount
		}

		// Build fee-payer transaction.
		rawTxnWithData, err := client.BuildFeePayerTransaction(senderAddr, senderAddr, payload, maxGas)
		if err != nil {
			logger.Error("build transaction failed", "error", err)
			errorResponse(w, http.StatusInternalServerError, "failed to build transaction")
			return
		}

		// Sign via Circle (sender = fee-payer = same wallet, so one signature covers both for now).
		auth, err := signer.SignTransaction(r.Context(), rawTxnWithData, wallet.WalletID, wallet.PublicKey)
		if err != nil {
			logger.Warn("WALLET", "wallet", wallet.WalletID)
			logger.Warn("TRANSACTION", "error", rawTxnWithData)
			logger.Error("sign transaction failed", "error", err)
			errorResponse(w, http.StatusInternalServerError, "failed to sign transaction")
			return
		}

		// signedTxn, ok := rawTxnWithData.ToFeePayerSignedTransaction(auth, auth, []crypto.AccountAuthenticator{})
		signedTxn, ok := rawTxnWithData.ToFeePayerSignedTransaction(auth, auth, []crypto.AccountAuthenticator{})
		if !ok {
			logger.Error("failed to build transaction", "error", err)
			errorResponse(w, http.StatusInternalServerError, "failed to build signed transaction")
			return
		}

		// Create transaction record.
		now := time.Now().UTC()
		rec := &store.TransactionRecord{
			ID:            uuid.New().String(),
			Status:        store.StatusPending,
			SenderAddress: wallet.Address,
			FunctionID:    req.FunctionID,
			WalletID:      req.WalletID,
			WebhookURL:    req.WebhookURL,
			CreatedAt:     now,
			UpdatedAt:     now,
			ExpiresAt:     now.Add(time.Duration(cfg.TxnExpirationSeconds) * time.Second),
		}
		if err := st.Create(r.Context(), rec); err != nil {
			logger.Error("store create failed", "error", err)
			errorResponse(w, http.StatusInternalServerError, "failed to store transaction")
			return
		}

		pubkey := &crypto.Ed25519PublicKey{}
		err = pubkey.FromHex(wallet.PublicKey)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "invalid public key")
		}
		msg, _ := rawTxnWithData.SigningMessage()
		err = signedTxn.Verify()
		if err != nil {
			logger.Error("failed to verify signed transaction", "error", err)
		}
		b := signedTxn.Authenticator.Verify(msg)
		if !b {
			logger.Error("failed to verify signed transaction from transaction", "error", b)
		}

		// Submit transaction.
		submitResp, err := client.SubmitTransaction(signedTxn)
		if err != nil {
			logger.Error("submit transaction failed", "Txn", signedTxn)
			logger.Error("submit transaction failed", "error", err)
			rec.Status = store.StatusFailed
			rec.ErrorMessage = err.Error()
			rec.UpdatedAt = time.Now().UTC()
			_ = st.Update(r.Context(), rec)
			errorResponse(w, http.StatusInternalServerError, "failed to submit transaction")
			return
		}

		// Update record to submitted with txn hash.
		rec.Status = store.StatusSubmitted
		rec.TxnHash = submitResp.Hash
		rec.UpdatedAt = time.Now().UTC()
		if err := st.Update(r.Context(), rec); err != nil {
			logger.Error("store update failed", "error", err)
		}

		jsonResponse(w, http.StatusAccepted, map[string]string{
			"transaction_id": rec.ID,
			"status":         string(rec.Status),
			"txn_hash":       rec.TxnHash,
		})
	}
}
