package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/internal/config"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/google/uuid"
)

type executeRequest struct {
	WalletID       string        `json:"wallet_id"`
	Address        string        `json:"address"`
	FunctionID     string        `json:"function_id"`
	TypeArguments  []string      `json:"type_arguments"`
	Arguments      []any         `json:"arguments"`
	MaxGasAmount   *uint64       `json:"max_gas_amount,omitempty"`
	WebhookURL     string        `json:"webhook_url,omitempty"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
	FeePayer       *feePayerInfo `json:"fee_payer,omitempty"`
}

type feePayerInfo struct {
	WalletID string `json:"wallet_id"`
	Address  string `json:"address"`
}

// Execute handles POST /v1/execute — enqueues a transaction for the background submitter.
func Execute(cfg *config.Config, st store.Store, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req executeRequest
		if err := decodeJSON(r, &req); err != nil {
			errorResponse(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		idempKey := req.IdempotencyKey
		if idempKey == "" {
			idempKey = r.Header.Get("Idempotency-Key")
		}
		if idempKey != "" {
			if existing, err := st.GetByIdempotencyKey(r.Context(), idempKey); err != nil {
				errorResponse(w, http.StatusInternalServerError, "failed to check idempotency")
				return
			} else if existing != nil {
				body := idempotentExecuteResponse(existing)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Idempotency-Replayed", "true")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write(body)
				return
			}
		}

		if req.WebhookURL != "" {
			if err := ValidateWebhookURL(req.WebhookURL); err != nil {
				errorResponse(w, http.StatusBadRequest, err.Error())
				return
			}
		}

		if req.WalletID == "" {
			errorResponse(w, http.StatusBadRequest, "wallet_id is required")
			return
		}
		if req.Address == "" {
			errorResponse(w, http.StatusBadRequest, "address is required")
			return
		}
		if req.FunctionID == "" {
			errorResponse(w, http.StatusBadRequest, "function_id is required")
			return
		}
		if parts := strings.Split(req.FunctionID, "::"); len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			errorResponse(w, http.StatusBadRequest, "function_id must be in format <address>::<module>::<function>")
			return
		}

		senderAddr, err := aptos.ParseAddress(req.Address)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "invalid address: "+err.Error())
			return
		}
		canonicalSender := senderAddr.StringLong()

		var feePayerWalletID, feePayerAddress string
		if req.FeePayer != nil {
			if req.FeePayer.WalletID == "" {
				errorResponse(w, http.StatusBadRequest, "fee_payer.wallet_id is required")
				return
			}
			if req.FeePayer.Address == "" {
				errorResponse(w, http.StatusBadRequest, "fee_payer.address is required")
				return
			}
			fpAddr, err := aptos.ParseAddress(req.FeePayer.Address)
			if err != nil {
				errorResponse(w, http.StatusBadRequest, "invalid fee_payer.address: "+err.Error())
				return
			}
			feePayerWalletID = req.FeePayer.WalletID
			feePayerAddress = fpAddr.StringLong()
		}

		qp := store.QueuedPayload{
			TypeArguments:    req.TypeArguments,
			Arguments:        req.Arguments,
			FeePayerWalletID: feePayerWalletID,
			FeePayerAddress:  feePayerAddress,
		}
		payloadJSON, err := json.Marshal(qp)
		if err != nil {
			errorResponse(w, http.StatusInternalServerError, "failed to encode payload")
			return
		}

		now := time.Now().UTC()
		rec := &store.TransactionRecord{
			ID:               uuid.New().String(),
			IdempotencyKey:   idempKey,
			Status:           store.StatusQueued,
			SenderAddress:    canonicalSender,
			FunctionID:       req.FunctionID,
			WalletID:         req.WalletID,
			FeePayerWalletID: feePayerWalletID,
			FeePayerAddress:  feePayerAddress,
			PayloadJSON:      string(payloadJSON),
			MaxGasAmount:     req.MaxGasAmount,
			WebhookURL:       req.WebhookURL,
			CreatedAt:        now,
			UpdatedAt:        now,
			ExpiresAt:        now.Add(time.Duration(cfg.TxnExpirationSeconds()) * time.Second),
		}

		if err := st.Create(r.Context(), rec); err != nil {
			if errors.Is(err, store.ErrIdempotencyConflict) {
				existing, gerr := st.GetByIdempotencyKey(r.Context(), idempKey)
				if gerr != nil || existing == nil {
					errorResponse(w, http.StatusInternalServerError, "idempotency conflict")
					return
				}
				body := idempotentExecuteResponse(existing)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Idempotency-Replayed", "true")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write(body)
				return
			}
			logger.Error("store create failed", "error", err)
			errorResponse(w, http.StatusInternalServerError, "failed to store transaction")
			return
		}

		respBody := map[string]any{
			"transaction_id": rec.ID,
			"status":         string(rec.Status),
		}
		bodyBytes, err := json.Marshal(respBody)
		if err != nil {
			errorResponse(w, http.StatusInternalServerError, "failed to encode response")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(bodyBytes)
	}
}

func idempotentExecuteResponse(rec *store.TransactionRecord) []byte {
	m := map[string]any{
		"transaction_id": rec.ID,
		"status":         string(rec.Status),
	}
	if rec.TxnHash != "" {
		m["txn_hash"] = rec.TxnHash
	}
	if rec.SequenceNumber != nil {
		m["sequence_number"] = *rec.SequenceNumber
	}
	b, _ := json.Marshal(m)
	return b
}
