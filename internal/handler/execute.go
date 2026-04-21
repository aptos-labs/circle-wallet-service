package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/internal/circle"
	"github.com/aptos-labs/jc-contract-integration/internal/config"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/google/uuid"
)

// executeRequest is the JSON body accepted by POST /v1/execute.
//
// FunctionID is the fully-qualified Move entry function, e.g.
// "0x1::coin::transfer". TypeArguments and Arguments are passed through to the
// ABI decoder and serialized at submit time, not at enqueue time.
//
// IdempotencyKey (or the "Idempotency-Key" request header) lets a client retry
// a request without creating a duplicate transaction. See Execute for the
// replay semantics.
//
// FeePayer is optional; when absent the sender pays its own gas.
//
// PublicKey is optional: clients that already know the wallet's Ed25519 public
// key can pass it to skip the Circle API round-trip the submitter would
// otherwise make on first use of this wallet. The server validates that the
// key's authkey equals Address before seeding the cache — a bad key is a 400,
// not a silent cache poison.
type executeRequest struct {
	WalletID       string        `json:"wallet_id"`
	Address        string        `json:"address"`
	PublicKey      string        `json:"public_key,omitempty"`
	FunctionID     string        `json:"function_id"`
	TypeArguments  []string      `json:"type_arguments"`
	Arguments      []any         `json:"arguments"`
	MaxGasAmount   *uint64       `json:"max_gas_amount,omitempty"`
	WebhookURL     string        `json:"webhook_url,omitempty"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
	FeePayer       *feePayerInfo `json:"fee_payer,omitempty"`
}

// feePayerInfo identifies a Circle wallet that pays gas on behalf of the
// sender. The service signs once as the sender and once as the fee payer and
// assembles a fee-payer transaction (see internal/submitter.prepareRecord).
//
// PublicKey is optional with the same semantics as the top-level field: seed
// the cache now, skip the Circle lookup later.
type feePayerInfo struct {
	WalletID  string `json:"wallet_id"`
	Address   string `json:"address"`
	PublicKey string `json:"public_key,omitempty"`
}

// Execute handles POST /v1/execute — enqueues a transaction for the background
// submitter and returns 202 immediately.
//
// This handler does no blockchain work: no signing, no ABI lookup, no sequence
// number allocation. It only validates the request and writes a row to the
// transactions table with status="queued" and sequence_number=NULL. The
// submitter claims the row asynchronously and assigns a sequence number at
// that point; see TRANSACTION_PIPELINE.md for the full flow.
//
// Idempotency: if the request carries an Idempotency-Key (body field or
// header) and a row with that key already exists, the prior response is
// replayed with the "X-Idempotency-Replayed: true" header and no new row is
// created. A race where two concurrent requests carry the same key is resolved
// by the unique index on idempotency_key: the loser gets ErrIdempotencyConflict
// from the store and falls into the same replay path.
func Execute(cfg *config.Config, st store.Store, pkCache *circle.PublicKeyCache, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req executeRequest
		if err := decodeJSON(w, r, &req); err != nil {
			errorResponse(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		// Body field takes precedence over the header so clients that only
		// control the body (e.g. SDK users behind a proxy that strips headers)
		// can still get idempotent behavior.
		idempKey := req.IdempotencyKey
		if idempKey == "" {
			idempKey = r.Header.Get("Idempotency-Key")
		}
		// First idempotency check: cheap lookup before we do any other work.
		// If this misses, a second check happens after Create returns
		// ErrIdempotencyConflict (the race path).
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

		// Canonicalize the sender address to long-form hex so queries against
		// transactions.sender_address (e.g. ListQueuedSenders, ClaimNext...) are
		// consistent. Without this, "0x1" and "0x000...001" would look like two
		// different senders and each get their own worker + sequence counter.
		senderAddr, err := aptos.ParseAddress(req.Address)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "invalid address: "+err.Error())
			return
		}
		canonicalSender := senderAddr.StringLong()

		// If the client provided a public key, verify it matches the address
		// (authkey == address) before seeding the cache. We refuse silently-bad
		// keys here so a typo becomes a 4xx on this request instead of a
		// delayed signing failure on the next.
		if req.PublicKey != "" {
			if err := verifyWalletPublicKey(req.Address, req.PublicKey); err != nil {
				errorResponse(w, http.StatusBadRequest, "public_key mismatch: "+err.Error())
				return
			}
		}

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
			if req.FeePayer.PublicKey != "" {
				if err := verifyWalletPublicKey(req.FeePayer.Address, req.FeePayer.PublicKey); err != nil {
					errorResponse(w, http.StatusBadRequest, "fee_payer.public_key mismatch: "+err.Error())
					return
				}
			}
			feePayerWalletID = req.FeePayer.WalletID
			feePayerAddress = fpAddr.StringLong()
		}

		// Seed the cache after all validation passes (not before) so a bad
		// fee-payer field doesn't leave a sender key half-committed.
		if req.PublicKey != "" && pkCache != nil {
			pkCache.Set(req.WalletID, req.PublicKey)
		}
		if req.FeePayer != nil && req.FeePayer.PublicKey != "" && pkCache != nil {
			pkCache.Set(req.FeePayer.WalletID, req.FeePayer.PublicKey)
		}

		qp := store.QueuedPayload{
			TypeArguments: req.TypeArguments,
			Arguments:     req.Arguments,
		}
		payloadJSON, err := json.Marshal(qp)
		if err != nil {
			errorResponse(w, http.StatusInternalServerError, "failed to encode payload")
			return
		}

		// created_at is assigned by the application (not MySQL's CURRENT_TIMESTAMP)
		// because it's the primary ordering key for the per-sender FIFO queue —
		// see the ORDER BY in ClaimNextQueuedForSender. Using the app clock keeps
		// the timestamp in the same timezone across rows and lets tests inject
		// deterministic times without stubbing MySQL.
		// sequence_number is intentionally left zero/unset: the submitter
		// allocates it atomically when it claims the row.
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

		// Second idempotency check (race path): two concurrent requests with the
		// same key both pass the initial lookup, then one of them loses to the
		// UNIQUE constraint on uk_idempotency. The loser converts the conflict
		// into a replay of the winner's record so both clients see identical
		// responses.
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

// verifyWalletPublicKey confirms that publicKeyHex's authkey equals address,
// reusing the same authkey-derivation logic the submitter applies right before
// signing. Keeping these two checks consistent is load-bearing: the submitter
// will refuse to sign if they disagree, so catching it here avoids letting a
// doomed record enter the queue.
func verifyWalletPublicKey(address, publicKeyHex string) error {
	wallet := &config.CircleWallet{Address: address, PublicKey: publicKeyHex}
	return wallet.VerifyWallet()
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
