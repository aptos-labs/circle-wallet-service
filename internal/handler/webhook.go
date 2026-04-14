package handler

import (
	"net/http"

	"github.com/aptos-labs/jc-contract-integration/internal/webhook"
)

// ListWebhookDeliveries handles GET /v1/transactions/{id}/webhooks.
// Returns all delivery attempts for a transaction, ordered by creation time.
func ListWebhookDeliveries(ws webhook.WebhookStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		txnID := r.PathValue("id")
		if txnID == "" {
			errorResponse(w, http.StatusBadRequest, "missing transaction id")
			return
		}

		records, err := ws.ListByTransactionID(r.Context(), txnID)
		if err != nil {
			errorResponse(w, http.StatusInternalServerError, "failed to list webhook deliveries")
			return
		}

		jsonResponse(w, http.StatusOK, records)
	}
}
