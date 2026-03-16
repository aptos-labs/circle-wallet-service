package handler

import (
	"net/http"

	"github.com/aptos-labs/jc-contract-integration/internal/api"
	"github.com/aptos-labs/jc-contract-integration/internal/txn"
)

// GetTransaction handles GET /v1/transactions/{id}.
func GetTransaction(mgr txn.Submitter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			api.Error(w, http.StatusBadRequest, "transaction id is required")
			return
		}

		rec, err := mgr.GetTransaction(r.Context(), id)
		if err != nil {
			api.Error(w, http.StatusInternalServerError, "query transaction: "+err.Error())
			return
		}
		if rec == nil {
			api.Error(w, http.StatusNotFound, "transaction not found")
			return
		}

		api.JSON(w, http.StatusOK, rec)
	}
}
