package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

func TestGetTransaction(t *testing.T) {
	st := newTestMemoryStore(t)
	now := time.Now().UTC()
	rec := &store.TransactionRecord{
		ID:            "tid-1",
		Status:        store.StatusQueued,
		SenderAddress: "0x1",
		FunctionID:    "0x1::m::f",
		WalletID:      "w",
		CreatedAt:     now,
		UpdatedAt:     now,
		ExpiresAt:     now.Add(time.Hour),
	}
	if err := st.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}

	h := GetTransaction(st)
	req := httptest.NewRequest(http.MethodGet, "/v1/transactions/tid-1", nil)
	req.SetPathValue("id", "tid-1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 got %d: %s", rr.Code, rr.Body.String())
	}
	var out store.TransactionRecord
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "tid-1" || out.Status != store.StatusQueued {
		t.Fatalf("record: %#v", out)
	}
}

func TestGetTransactionNotFound(t *testing.T) {
	st := newTestMemoryStore(t)
	h := GetTransaction(st)
	req := httptest.NewRequest(http.MethodGet, "/v1/transactions/missing", nil)
	req.SetPathValue("id", "missing")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetTransactionInvalidID(t *testing.T) {
	st := newTestMemoryStore(t)
	h := GetTransaction(st)
	req := httptest.NewRequest(http.MethodGet, "/v1/transactions/", nil)
	req.SetPathValue("id", "")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}
