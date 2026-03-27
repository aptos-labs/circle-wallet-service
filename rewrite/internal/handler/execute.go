package handler

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/google/uuid"

	aptosint "github.com/aptos-labs/jc-contract-integration/rewrite/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/rewrite/internal/circle"
	"github.com/aptos-labs/jc-contract-integration/rewrite/internal/config"
	"github.com/aptos-labs/jc-contract-integration/rewrite/internal/store"
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
	client *aptosint.Client,
	abiCache *aptosint.ABICache,
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

		// Look up wallet from config.
		wallet, ok := cfg.WalletByID(req.WalletID)
		if !ok {
			errorResponse(w, http.StatusBadRequest, "unknown wallet_id")
			return
		}

		// Parse function ID.
		addr, module, function, err := aptosint.ParseFunctionID(req.FunctionID)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}

		// Resolve ABI params and validate argument count.
		params, err := abiCache.GetFunctionParams(addr, module, function)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "resolve ABI: "+err.Error())
			return
		}
		if len(req.Arguments) != len(params) {
			errorResponse(w, http.StatusBadRequest, "argument count mismatch: expected "+
				itoa(len(params))+", got "+itoa(len(req.Arguments)))
			return
		}

		// BCS-serialize each argument.
		args := make([][]byte, len(req.Arguments))
		for i, arg := range req.Arguments {
			b, err := aptosint.SerializeArgument(params[i], arg)
			if err != nil {
				errorResponse(w, http.StatusBadRequest, "argument["+itoa(i)+"]: "+err.Error())
				return
			}
			args[i] = b
		}

		// Parse type arguments (default to empty slice).
		typeTags, err := aptosint.ParseTypeTags(req.TypeArguments)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		if typeTags == nil {
			typeTags = []aptossdk.TypeTag{}
		}

		// Build EntryFunction payload.
		modAddr, err := aptosint.ParseAddress(addr)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "invalid module address: "+err.Error())
			return
		}
		payload := aptossdk.TransactionPayload{
			Payload: &aptossdk.EntryFunction{
				Module: aptossdk.ModuleId{
					Address: modAddr,
					Name:    module,
				},
				Function: function,
				ArgTypes: typeTags,
				Args:     args,
			},
		}

		// Parse sender address from wallet.
		senderAddr, err := aptosint.ParseAddress(wallet.Address)
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
		rawTxnWithData, err := client.BuildFeePayerTransaction(senderAddr, payload, maxGas)
		if err != nil {
			logger.Error("build transaction failed", "error", err)
			errorResponse(w, http.StatusInternalServerError, "failed to build transaction")
			return
		}

		// Ensure public key has 0x prefix.
		pubKeyHex := wallet.PublicKey
		if !strings.HasPrefix(pubKeyHex, "0x") {
			pubKeyHex = "0x" + pubKeyHex
		}

		// Sign via Circle.
		signedTxn, err := signer.SignFeePayerTransaction(r.Context(), rawTxnWithData, wallet.WalletID, pubKeyHex, senderAddr)
		if err != nil {
			logger.Error("sign transaction failed", "error", err)
			errorResponse(w, http.StatusInternalServerError, "failed to sign transaction")
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

		// Submit transaction.
		submitResp, err := client.SubmitTransaction(signedTxn)
		if err != nil {
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
