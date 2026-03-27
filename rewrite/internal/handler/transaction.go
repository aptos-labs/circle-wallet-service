package handler

import (
	"net/http"

	"github.com/aptos-labs/jc-contract-integration/rewrite/internal/store"
)

// GetTransaction handles GET /v1/transactions/{id}.
func GetTransaction(st store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			errorResponse(w, http.StatusBadRequest, "missing transaction id")
			return
		}

		rec, err := st.Get(r.Context(), id)
		if err != nil {
			errorResponse(w, http.StatusInternalServerError, "failed to retrieve transaction")
			return
		}
		if rec == nil {
			errorResponse(w, http.StatusNotFound, "transaction not found")
			return
		}

		jsonResponse(w, http.StatusOK, rec)
	}
}
