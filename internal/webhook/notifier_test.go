package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

type mockWebhookStore struct {
	mu         sync.Mutex
	deliveries []*DeliveryRecord
	createErr  error
	claim      []*DeliveryRecord
	claimErr   error
	updates    []*DeliveryRecord
	updateErr  error
	listByTxn  map[string][]*DeliveryRecord
}

func (m *mockWebhookStore) CreateDelivery(_ context.Context, rec *DeliveryRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	cp := *rec
	m.deliveries = append(m.deliveries, &cp)
	return nil
}

func (m *mockWebhookStore) ClaimPendingDeliveries(_ context.Context, _ int) ([]*DeliveryRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimErr != nil {
		return nil, m.claimErr
	}
	return m.claim, nil
}

func (m *mockWebhookStore) UpdateDelivery(_ context.Context, rec *DeliveryRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateErr != nil {
		return m.updateErr
	}
	cp := *rec
	m.updates = append(m.updates, &cp)
	return nil
}

func (m *mockWebhookStore) ListByTransactionID(_ context.Context, txnID string) ([]*DeliveryRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listByTxn == nil {
		return nil, nil
	}
	return m.listByTxn[txnID], nil
}

func (m *mockWebhookStore) RecoverStaleDeliveries(_ context.Context, _ time.Duration) (int64, error) {
	return 0, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNotifyInsertsRecord(t *testing.T) {
	t.Parallel()
	ms := &mockWebhookStore{}
	n := NewWebhookNotifier("", ms, testLogger())
	rec := &store.TransactionRecord{
		ID:            "txn-1",
		Status:        store.StatusConfirmed,
		TxnHash:       "0xhash",
		SenderAddress: "0xsender",
		FunctionID:    "0x1::m::f",
		WalletID:      "w1",
		WebhookURL:    "https://hooks.example/r",
	}
	n.Notify(context.Background(), rec)
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.deliveries) != 1 {
		t.Fatalf("deliveries: got %d, want 1", len(ms.deliveries))
	}
	d := ms.deliveries[0]
	if d.Status != "pending" {
		t.Errorf("status = %q, want pending", d.Status)
	}
	if d.URL != rec.WebhookURL {
		t.Errorf("URL = %q, want %q", d.URL, rec.WebhookURL)
	}
	if d.TransactionID != rec.ID {
		t.Errorf("TransactionID = %q", d.TransactionID)
	}
}

func TestNotifyUsesGlobalURL(t *testing.T) {
	t.Parallel()
	ms := &mockWebhookStore{}
	global := "https://global.example/webhook"
	n := NewWebhookNotifier(global, ms, testLogger())
	rec := &store.TransactionRecord{
		ID:            "txn-2",
		Status:        store.StatusQueued,
		SenderAddress: "0xs",
		FunctionID:    "0x1::m::f",
		WalletID:      "w",
	}
	n.Notify(context.Background(), rec)
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.deliveries) != 1 {
		t.Fatalf("deliveries: got %d, want 1", len(ms.deliveries))
	}
	if ms.deliveries[0].URL != global {
		t.Errorf("URL = %q, want %q", ms.deliveries[0].URL, global)
	}
}

func TestNotifyNoURL(t *testing.T) {
	t.Parallel()
	ms := &mockWebhookStore{}
	n := NewWebhookNotifier("", ms, testLogger())
	rec := &store.TransactionRecord{
		ID:            "txn-3",
		Status:        store.StatusQueued,
		SenderAddress: "0xs",
		FunctionID:    "0x1::m::f",
		WalletID:      "w",
	}
	n.Notify(context.Background(), rec)
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.deliveries) != 0 {
		t.Fatalf("deliveries: got %d, want 0", len(ms.deliveries))
	}
}

func TestPayloadContainsExpectedFields(t *testing.T) {
	t.Parallel()
	ms := &mockWebhookStore{}
	n := NewWebhookNotifier("https://hook/x", ms, testLogger())
	rec := &store.TransactionRecord{
		ID:            "txn-4",
		Status:        store.StatusSubmitted,
		TxnHash:       "0xabc",
		SenderAddress: "0xsender",
		FunctionID:    "0x2::mod::entry",
		WalletID:      "w",
	}
	n.Notify(context.Background(), rec)
	ms.mu.Lock()
	if len(ms.deliveries) != 1 {
		ms.mu.Unlock()
		t.Fatalf("deliveries: got %d", len(ms.deliveries))
	}
	raw := ms.deliveries[0].Payload
	ms.mu.Unlock()

	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"transaction_id", "status", "txn_hash", "sender_address", "function_id"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	if string(m["transaction_id"]) != `"txn-4"` {
		t.Errorf("transaction_id = %s", m["transaction_id"])
	}
	if string(m["txn_hash"]) != `"0xabc"` {
		t.Errorf("txn_hash = %s", m["txn_hash"])
	}
	if string(m["sender_address"]) != `"0xsender"` {
		t.Errorf("sender_address = %s", m["sender_address"])
	}
	if string(m["function_id"]) != `"0x2::mod::entry"` {
		t.Errorf("function_id = %s", m["function_id"])
	}
	if _, ok := m["timestamp"]; !ok {
		t.Error("missing timestamp")
	}
}

func TestNotifierPayloadFormat(t *testing.T) {
	t.Parallel()
	ms := &mockWebhookStore{}
	n := NewWebhookNotifier("https://hook/x", ms, testLogger())
	rec := &store.TransactionRecord{
		ID:            "txn-fmt",
		Status:        store.StatusFailed,
		TxnHash:       "0xdead",
		ErrorMessage:  "move_abort",
		SenderAddress: "0xsender",
		FunctionID:    "0x3::m::f",
		WalletID:      "w",
		WebhookURL:    "https://hooks.example/r",
	}
	n.Notify(context.Background(), rec)
	ms.mu.Lock()
	if len(ms.deliveries) != 1 {
		ms.mu.Unlock()
		t.Fatalf("deliveries: %d", len(ms.deliveries))
	}
	raw := ms.deliveries[0].Payload
	ms.mu.Unlock()

	var p Payload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if p.TransactionID != rec.ID || p.Status != rec.Status || p.TxnHash != rec.TxnHash {
		t.Fatalf("payload mismatch: %+v", p)
	}
	if p.ErrorMessage != rec.ErrorMessage || p.SenderAddress != rec.SenderAddress || p.FunctionID != rec.FunctionID {
		t.Fatalf("payload fields: %+v", p)
	}
	if p.Timestamp.IsZero() {
		t.Fatal("timestamp zero")
	}
}

func TestNotifierPayloadFormatWithoutOptionalHashes(t *testing.T) {
	t.Parallel()
	ms := &mockWebhookStore{}
	n := NewWebhookNotifier("https://hook/x", ms, testLogger())
	rec := &store.TransactionRecord{
		ID:            "txn-fmt2",
		Status:        store.StatusQueued,
		SenderAddress: "0xs",
		FunctionID:    "0x1::m::f",
		WalletID:      "w",
		WebhookURL:    "https://hooks.example/r",
	}
	n.Notify(context.Background(), rec)
	ms.mu.Lock()
	raw := ms.deliveries[0].Payload
	ms.mu.Unlock()

	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["txn_hash"]; ok {
		t.Error("expected txn_hash omitted")
	}
	if _, ok := m["error_message"]; ok {
		t.Error("expected error_message omitted")
	}
}
