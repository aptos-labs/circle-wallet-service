package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/internal/config"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/google/uuid"
)

// walletInfo allows callers to supply wallet credentials inline rather
// than relying on pre-configured wallets in CIRCLE_WALLETS.
type walletInfo struct {
	WalletID  string `json:"wallet_id"`
	Address   string `json:"address"`
	PublicKey string `json:"public_key"`
}

type executeRequest struct {
	WalletID       string      `json:"wallet_id"`
	Wallet         *walletInfo `json:"wallet,omitempty"`
	FunctionID     string      `json:"function_id"`
	TypeArguments  []string    `json:"type_arguments"`
	Arguments      []any       `json:"arguments"`
	MaxGasAmount   *uint64     `json:"max_gas_amount,omitempty"`
	WebhookURL     string      `json:"webhook_url,omitempty"`
	IdempotencyKey string      `json:"idempotency_key,omitempty"`
}

// resolveWallet returns the Circle wallet to use for this request.
func resolveWallet(cfg *config.Config, req *executeRequest) (config.CircleWallet, bool) {
	if req.Wallet != nil {
		w := config.CircleWallet{
			WalletID:  req.Wallet.WalletID,
			Address:   req.Wallet.Address,
			PublicKey: req.Wallet.PublicKey,
		}
		if w.WalletID == "" || w.Address == "" || w.PublicKey == "" {
			return config.CircleWallet{}, false
		}
		return w, true
	}
	if req.WalletID != "" {
		return cfg.LookupWallet(req.WalletID)
	}
	return config.CircleWallet{}, false
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

		if req.WalletID == "" && req.Wallet == nil {
			errorResponse(w, http.StatusBadRequest, "wallet_id or wallet object is required")
			return
		}
		if req.FunctionID == "" {
			errorResponse(w, http.StatusBadRequest, "function_id is required")
			return
		}

		wallet, ok := resolveWallet(cfg, &req)
		if !ok {
			errorResponse(w, http.StatusBadRequest, "unknown wallet_id/address or incomplete inline wallet (wallet_id, address, public_key required)")
			return
		}
		if req.Wallet != nil {
			if err := wallet.VerifyWallet(); err != nil {
				errorResponse(w, http.StatusBadRequest, "invalid wallet: "+err.Error())
				return
			}
		}

		senderAddr, err := aptos.ParseAddress(wallet.Address)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "invalid wallet address: "+err.Error())
			return
		}
		canonicalSender := senderAddr.StringLong()

		qp := store.QueuedPayload{
			TypeArguments: req.TypeArguments,
			Arguments:     req.Arguments,
		}
		if req.Wallet != nil {
			qp.Wallet = &store.WalletField{
				WalletID:  req.Wallet.WalletID,
				Address:   req.Wallet.Address,
				PublicKey: req.Wallet.PublicKey,
			}
		}
		payloadJSON, err := json.Marshal(qp)
		if err != nil {
			errorResponse(w, http.StatusInternalServerError, "failed to encode payload")
			return
		}

		now := time.Now().UTC()
		rec := &store.TransactionRecord{
			ID:             uuid.New().String(),
			IdempotencyKey: idempKey,
			Status:         store.StatusQueued,
			SenderAddress:  canonicalSender,
			FunctionID:     req.FunctionID,
			WalletID:       wallet.WalletID,
			PayloadJSON:    string(payloadJSON),
			MaxGasAmount:   req.MaxGasAmount,
			WebhookURL:     req.WebhookURL,
			CreatedAt:      now,
			UpdatedAt:      now,
			ExpiresAt:      now.Add(time.Duration(cfg.TxnExpirationSeconds) * time.Second),
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
