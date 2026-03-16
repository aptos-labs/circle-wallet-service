package handler

import (
	"net/http"

	"github.com/aptos-labs/jc-contract-integration/internal/api"
	"github.com/aptos-labs/jc-contract-integration/internal/txn"
)

type submitResponse struct {
	TransactionID string `json:"transaction_id"`
	Status        string `json:"status"`
}

// submitAndRespond submits an operation via the manager and writes the 202 response.
func submitAndRespond(w http.ResponseWriter, r *http.Request, mgr txn.Submitter, op txn.Operation) {
	txnID, err := mgr.Submit(r.Context(), op)
	if err != nil {
		api.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.JSON(w, http.StatusAccepted, submitResponse{
		TransactionID: txnID,
		Status:        "pending",
	})
}
