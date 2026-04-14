package store

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStore_CreateAndGet(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	rec := &TransactionRecord{
		ID:            "txn-1",
		Status:        StatusQueued,
		SenderAddress: "0xabc",
		FunctionID:    "0x1::module::func",
		WalletID:      "wallet-1",
		PayloadJSON:   "{}",
	}

	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(ctx, "txn-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.ID != "txn-1" {
		t.Errorf("ID = %q, want %q", got.ID, "txn-1")
	}
	if got.Status != StatusQueued {
		t.Errorf("Status = %q, want %q", got.Status, StatusQueued)
	}
	if got.SenderAddress != "0xabc" {
		t.Errorf("SenderAddress = %q, want %q", got.SenderAddress, "0xabc")
	}
	if got.FunctionID != "0x1::module::func" {
		t.Errorf("FunctionID = %q, want %q", got.FunctionID, "0x1::module::func")
	}
	if got.WalletID != "wallet-1" {
		t.Errorf("WalletID = %q, want %q", got.WalletID, "wallet-1")
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}

func TestMemoryStore_CreateDuplicate(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	rec := &TransactionRecord{
		ID:            "txn-dup",
		Status:        StatusQueued,
		SenderAddress: "0xabc",
		FunctionID:    "0x1::module::func",
		WalletID:      "wallet-1",
		PayloadJSON:   "{}",
	}

	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := s.Create(ctx, rec); err == nil {
		t.Fatal("second Create should have returned an error")
	}
}

func TestMemoryStore_Update(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	rec := &TransactionRecord{
		ID:            "txn-upd",
		Status:        StatusQueued,
		SenderAddress: "0xabc",
		FunctionID:    "0x1::module::func",
		WalletID:      "wallet-1",
		PayloadJSON:   "{}",
	}

	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	created, _ := s.Get(ctx, "txn-upd")

	updated := *created
	updated.Status = StatusSubmitted
	updated.TxnHash = "0xhash123"

	if err := s.Update(ctx, &updated); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Get(ctx, "txn-upd")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Status != StatusSubmitted {
		t.Errorf("Status = %q, want %q", got.Status, StatusSubmitted)
	}
	if got.TxnHash != "0xhash123" {
		t.Errorf("TxnHash = %q, want %q", got.TxnHash, "0xhash123")
	}
	if !got.UpdatedAt.After(created.UpdatedAt) {
		t.Error("UpdatedAt should be later than original")
	}
}

func TestMemoryStore_ListByStatus(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	records := []*TransactionRecord{
		{ID: "txn-a", Status: StatusSubmitted, SenderAddress: "0x1", FunctionID: "f", WalletID: "w", PayloadJSON: "{}"},
		{ID: "txn-b", Status: StatusSubmitted, SenderAddress: "0x2", FunctionID: "f", WalletID: "w", PayloadJSON: "{}"},
		{ID: "txn-c", Status: StatusQueued, SenderAddress: "0x3", FunctionID: "f", WalletID: "w", PayloadJSON: "{}"},
	}
	for _, r := range records {
		if err := s.Create(ctx, r); err != nil {
			t.Fatalf("Create %s: %v", r.ID, err)
		}
	}

	list, err := s.ListByStatus(ctx, StatusSubmitted)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d records, want 2", len(list))
	}
}

func TestMemoryStore_GetNotFound(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	got, err := s.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestMemoryStore_GetByIdempotencyKey(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	rec := &TransactionRecord{
		ID:             "txn-idemp",
		IdempotencyKey: "key-abc",
		Status:         StatusSubmitted,
		SenderAddress:  "0xabc",
		FunctionID:     "0x1::module::func",
		WalletID:       "wallet-1",
		PayloadJSON:    "{}",
	}

	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.GetByIdempotencyKey(ctx, "key-abc")
	if err != nil {
		t.Fatalf("GetByIdempotencyKey: %v", err)
	}
	if got == nil {
		t.Fatal("expected record, got nil")
	}
	if got.ID != "txn-idemp" {
		t.Errorf("ID = %q, want %q", got.ID, "txn-idemp")
	}

	got2, err := s.GetByIdempotencyKey(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetByIdempotencyKey miss: %v", err)
	}
	if got2 != nil {
		t.Fatalf("expected nil, got %+v", got2)
	}

	got3, err := s.GetByIdempotencyKey(ctx, "")
	if err != nil {
		t.Fatalf("GetByIdempotencyKey empty: %v", err)
	}
	if got3 != nil {
		t.Fatalf("expected nil for empty key, got %+v", got3)
	}
}

func TestMemoryStore_Eviction(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(50 * time.Millisecond)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	rec := &TransactionRecord{
		ID:            "txn-evict",
		Status:        StatusQueued,
		SenderAddress: "0xabc",
		FunctionID:    "0x1::module::func",
		WalletID:      "wallet-1",
		PayloadJSON:   "{}",
	}

	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	s.evict()

	got, err := s.Get(ctx, "txn-evict")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatal("expected record to be evicted, but it still exists")
	}
}

func TestMemoryStore_UpdateIfStatus(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	rec := &TransactionRecord{
		ID:            "txn-if",
		Status:        StatusQueued,
		SenderAddress: "0xabc",
		FunctionID:    "0x1::m::f",
		WalletID:      "w",
		PayloadJSON:   "{}",
	}
	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated := *rec
	updated.Status = StatusSubmitted
	updated.TxnHash = "0xh"
	ok, err := s.UpdateIfStatus(ctx, &updated, StatusSubmitted)
	if err != nil {
		t.Fatalf("UpdateIfStatus wrong expected: %v", err)
	}
	if ok {
		t.Fatal("UpdateIfStatus should return false when status mismatch")
	}
	got, _ := s.Get(ctx, "txn-if")
	if got.Status != StatusQueued {
		t.Errorf("status = %q, want queued", got.Status)
	}

	updated2 := *got
	updated2.Status = StatusSubmitted
	updated2.TxnHash = "0xh2"
	ok2, err := s.UpdateIfStatus(ctx, &updated2, StatusQueued)
	if err != nil {
		t.Fatalf("UpdateIfStatus: %v", err)
	}
	if !ok2 {
		t.Fatal("UpdateIfStatus should succeed when status matches")
	}
	got2, _ := s.Get(ctx, "txn-if")
	if got2.Status != StatusSubmitted || got2.TxnHash != "0xh2" {
		t.Errorf("after update: %+v", got2)
	}
}

func TestMemoryStore_FeePayerFields(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	rec := &TransactionRecord{
		ID:               "txn-fp",
		Status:           StatusQueued,
		SenderAddress:    "0xs",
		FunctionID:       "0x1::m::f",
		WalletID:         "w",
		FeePayerWalletID: "fp-wallet",
		FeePayerAddress:  "0xfee",
		PayloadJSON:      "{}",
	}
	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, "txn-fp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FeePayerWalletID != "fp-wallet" {
		t.Errorf("FeePayerWalletID = %q", got.FeePayerWalletID)
	}
	if got.FeePayerAddress != "0xfee" {
		t.Errorf("FeePayerAddress = %q", got.FeePayerAddress)
	}
}

