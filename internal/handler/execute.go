package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
	"github.com/aptos-labs/jc-contract-integration/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/internal/circle"
	"github.com/aptos-labs/jc-contract-integration/internal/config"
	"github.com/aptos-labs/jc-contract-integration/internal/idempotency"
	"github.com/aptos-labs/jc-contract-integration/internal/nonce"
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
	// Wallet identification — either wallet_id (looked up from config) or
	// inline wallet object. If both are provided, the inline wallet takes
	// precedence.
	WalletID string      `json:"wallet_id"`
	Wallet   *walletInfo `json:"wallet,omitempty"`

	FunctionID    string   `json:"function_id"`
	TypeArguments []string `json:"type_arguments"`
	Arguments     []any    `json:"arguments"`
	MaxGasAmount  *uint64  `json:"max_gas_amount,omitempty"`
	WebhookURL    string   `json:"webhook_url,omitempty"`

	// When true (or when the server default is true and this is nil),
	// the transaction uses a replay-protection nonce instead of an
	// ordered sequence number.
	Orderless *bool `json:"orderless,omitempty"`

	// Optional client-provided idempotency key. If a request with the
	// same key was already processed, the cached response is returned.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// resolveWallet returns the Circle wallet to use for this request.
// Per-request wallet info takes precedence over config-based lookup.
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

// Execute handles POST /v1/execute.
func Execute(
	cfg *config.Config,
	client *aptos.Client,
	abiCache *aptos.ABICache,
	signer *circle.Signer,
	st store.Store,
	nonceStore *nonce.Store,
	idempotencyStore *idempotency.Store,
	logger *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req executeRequest
		if err := decodeJSON(r, &req); err != nil {
			errorResponse(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		// --- Idempotency check ---------------------------------------------------
		idempKey := req.IdempotencyKey
		if idempKey == "" {
			idempKey = r.Header.Get("Idempotency-Key")
		}
		if idempKey != "" {
			if cached := idempotencyStore.Get(idempKey); cached != nil {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Idempotency-Replayed", "true")
				w.WriteHeader(cached.StatusCode)
				_, _ = w.Write(cached.Body)
				return
			}
		}

		// --- Validate required fields -------------------------------------------
		if req.WalletID == "" && req.Wallet == nil {
			errorResponse(w, http.StatusBadRequest, "wallet_id or wallet object is required")
			return
		}
		if req.FunctionID == "" {
			errorResponse(w, http.StatusBadRequest, "function_id is required")
			return
		}

		// --- Resolve wallet -----------------------------------------------------
		wallet, ok := resolveWallet(cfg, &req)
		if !ok {
			errorResponse(w, http.StatusBadRequest, "unknown wallet_id/address or incomplete inline wallet (wallet_id, address, public_key required)")
			return
		}

		// Validate inline wallet the same way config-loaded wallets are verified.
		if req.Wallet != nil {
			if err := wallet.VerifyWallet(); err != nil {
				errorResponse(w, http.StatusBadRequest, "invalid wallet: "+err.Error())
				return
			}
		}

		// --- Determine orderless mode -------------------------------------------
		orderless := cfg.OrderlessEnabled
		if req.Orderless != nil {
			orderless = *req.Orderless
		}

		// --- Build payload ------------------------------------------------------
		entryFunction, err := abiCache.BuildEntryFunctionPayload(req.FunctionID, req.TypeArguments, req.Arguments)
		if err != nil {
			errorResponse(w, http.StatusInternalServerError, "failed to build entry function payload: "+err.Error())
			return
		}

		senderAddr, err := aptos.ParseAddress(wallet.Address)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "invalid wallet address: "+err.Error())
			return
		}

		var maxGas uint64
		if req.MaxGasAmount != nil {
			maxGas = *req.MaxGasAmount
		}

		// Use canonical long-form address for nonce tracking to avoid
		// case/format mismatches treating the same account as different.
		canonicalAddr := senderAddr.StringLong()

		// --- Build transaction (ordered vs orderless) ---------------------------
		var replayNonce string
		var rawTxnWithData *aptossdk.RawTransactionWithData
		if orderless {
			n, err := nonceStore.Generate(canonicalAddr)
			if err != nil {
				logger.Error("nonce generation failed", "error", err)
				errorResponse(w, http.StatusInternalServerError, "failed to generate replay nonce")
				return
			}
			replayNonce = store.FormatNonce(n)

			rawTxnWithData, err = client.BuildOrderlessFeePayerTransaction(senderAddr, senderAddr, entryFunction, n, maxGas)
			if err != nil {
				logger.Error("build orderless transaction failed", "error", err)
				errorResponse(w, http.StatusInternalServerError, "failed to build transaction")
				return
			}
		} else {
			payload := aptossdk.TransactionPayload{Payload: entryFunction}
			var err error
			rawTxnWithData, err = client.BuildFeePayerTransaction(senderAddr, senderAddr, payload, maxGas)
			if err != nil {
				logger.Error("build transaction failed", "error", err)
				errorResponse(w, http.StatusInternalServerError, "failed to build transaction")
				return
			}
		}

		// --- Sign via Circle ----------------------------------------------------
		auth, err := signer.SignTransaction(r.Context(), rawTxnWithData, wallet.WalletID, wallet.PublicKey)
		if err != nil {
			logger.Error("sign transaction failed", "error", err, "wallet", wallet.WalletID)
			errorResponse(w, http.StatusInternalServerError, "failed to sign transaction")
			return
		}

		// --- Assemble signed transaction ----------------------------------------
		signedTxn, ok := rawTxnWithData.ToFeePayerSignedTransaction(auth, auth, []crypto.AccountAuthenticator{})
		if !ok {
			logger.Error("failed to assemble fee-payer signed transaction")
			errorResponse(w, http.StatusInternalServerError, "failed to build signed transaction")
			return
		}

		// --- Create transaction record ------------------------------------------
		now := time.Now().UTC()
		rec := &store.TransactionRecord{
			ID:             uuid.New().String(),
			IdempotencyKey: idempKey,
			Status:         store.StatusPending,
			SenderAddress:  canonicalAddr,
			FunctionID:     req.FunctionID,
			WalletID:       wallet.WalletID,
			Orderless:      orderless,
			ReplayNonce:    replayNonce,
			WebhookURL:     req.WebhookURL,
			CreatedAt:      now,
			UpdatedAt:      now,
			ExpiresAt:      now.Add(time.Duration(cfg.TxnExpirationSeconds) * time.Second),
		}
		if err := st.Create(r.Context(), rec); err != nil {
			logger.Error("store create failed", "error", err)
			errorResponse(w, http.StatusInternalServerError, "failed to store transaction")
			return
		}

		// --- Verify signature (best-effort logging) -----------------------------
		if err := signedTxn.Verify(); err != nil {
			logger.Error("failed to verify signed transaction", "error", err)
		}
		if msg, err := rawTxnWithData.SigningMessage(); err == nil {
			if !signedTxn.Authenticator.Verify(msg) {
				logger.Error("authenticator verification failed")
			}
		}

		// --- Submit transaction -------------------------------------------------
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

		rec.Status = store.StatusSubmitted
		rec.TxnHash = submitResp.Hash
		rec.UpdatedAt = time.Now().UTC()
		if err := st.Update(r.Context(), rec); err != nil {
			logger.Error("store update failed", "error", err)
		}

		respBody := map[string]any{
			"transaction_id": rec.ID,
			"status":         string(rec.Status),
			"txn_hash":       rec.TxnHash,
			"orderless":      orderless,
		}
		if replayNonce != "" {
			respBody["replay_nonce"] = replayNonce
		}

		// Marshal once so the exact bytes are used for both the HTTP response
		// and the idempotency cache (avoids trailing-newline mismatch from
		// json.Encoder.Encode vs json.Marshal).
		bodyBytes, err := json.Marshal(respBody)
		if err != nil {
			logger.Error("marshal response failed", "error", err)
			errorResponse(w, http.StatusInternalServerError, "failed to encode response")
			return
		}

		if idempKey != "" {
			idempotencyStore.Set(idempKey, http.StatusAccepted, bodyBytes)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(bodyBytes)
	}
}
