package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/webhook"
)

type stubWebhookStore struct {
	byTxn map[string][]*webhook.DeliveryRecord
	err   error
}

func (s *stubWebhookStore) CreateDelivery(context.Context, *webhook.DeliveryRecord) error {
	return nil
}

func (s *stubWebhookStore) ClaimPendingDeliveries(context.Context, int) ([]*webhook.DeliveryRecord, error) {
	return nil, nil
}

func (s *stubWebhookStore) UpdateDelivery(context.Context, *webhook.DeliveryRecord) error {
	return nil
}

func (s *stubWebhookStore) RecoverStaleDeliveries(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

func (s *stubWebhookStore) ListByTransactionID(_ context.Context, txnID string) ([]*webhook.DeliveryRecord, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.byTxn == nil {
		return []*webhook.DeliveryRecord{}, nil
	}
	rec, ok := s.byTxn[txnID]
	if !ok || rec == nil {
		return []*webhook.DeliveryRecord{}, nil
	}
	return rec, nil
}

func TestListWebhookDeliveries(t *testing.T) {
	now := time.Now().UTC()
	d1 := &webhook.DeliveryRecord{
		ID:            "d1",
		TransactionID: "tx1",
		URL:           "https://h.example/a",
		Payload:       `{}`,
		Status:        "delivered",
		NextRetryAt:   now,
		CreatedAt:     now,
	}
	st := &stubWebhookStore{byTxn: map[string][]*webhook.DeliveryRecord{"tx1": {d1}}}

	h := ListWebhookDeliveries(st)
	req := httptest.NewRequest(http.MethodGet, "/v1/transactions/tx1/webhooks", nil)
	req.SetPathValue("id", "tx1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 got %d: %s", rr.Code, rr.Body.String())
	}
	var out []*webhook.DeliveryRecord
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "d1" {
		t.Fatalf("body: %#v", out)
	}
}

func TestListWebhookDeliveriesEmpty(t *testing.T) {
	st := &stubWebhookStore{byTxn: map[string][]*webhook.DeliveryRecord{}}

	h := ListWebhookDeliveries(st)
	req := httptest.NewRequest(http.MethodGet, "/v1/transactions/tx2/webhooks", nil)
	req.SetPathValue("id", "tx2")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 got %d: %s", rr.Code, rr.Body.String())
	}
	var out []*webhook.DeliveryRecord
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("want empty slice, got %#v", out)
	}
}

func TestListWebhookDeliveriesStoreError(t *testing.T) {
	st := &stubWebhookStore{err: errors.New("db")}
	h := ListWebhookDeliveries(st)
	req := httptest.NewRequest(http.MethodGet, "/v1/transactions/tx/webhooks", nil)
	req.SetPathValue("id", "tx")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestListWebhookDeliveriesMissingID(t *testing.T) {
	st := &stubWebhookStore{}
	h := ListWebhookDeliveries(st)
	req := httptest.NewRequest(http.MethodGet, "/v1/transactions//webhooks", nil)
	req.SetPathValue("id", "")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}
