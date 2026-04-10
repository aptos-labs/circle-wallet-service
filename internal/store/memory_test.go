package store

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStore_CreateAndGet(t *testing.T) {
	s := NewMemoryStore(time.Hour)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	rec := &TransactionRecord{
		ID:            "txn-1",
		Status:        StatusPending,
		SenderAddress: "0xabc",
		FunctionID:    "0x1::module::func",
		WalletID:      "wallet-1",
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
	if got.Status != StatusPending {
		t.Errorf("Status = %q, want %q", got.Status, StatusPending)
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
	s := NewMemoryStore(time.Hour)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	rec := &TransactionRecord{
		ID:            "txn-dup",
		Status:        StatusPending,
		SenderAddress: "0xabc",
		FunctionID:    "0x1::module::func",
		WalletID:      "wallet-1",
	}

	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := s.Create(ctx, rec); err == nil {
		t.Fatal("second Create should have returned an error")
	}
}

func TestMemoryStore_Update(t *testing.T) {
	s := NewMemoryStore(time.Hour)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	rec := &TransactionRecord{
		ID:            "txn-upd",
		Status:        StatusPending,
		SenderAddress: "0xabc",
		FunctionID:    "0x1::module::func",
		WalletID:      "wallet-1",
	}

	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Retrieve to get the CreatedAt set by Create.
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
	s := NewMemoryStore(time.Hour)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	records := []*TransactionRecord{
		{ID: "txn-a", Status: StatusSubmitted, SenderAddress: "0x1", FunctionID: "f", WalletID: "w"},
		{ID: "txn-b", Status: StatusSubmitted, SenderAddress: "0x2", FunctionID: "f", WalletID: "w"},
		{ID: "txn-c", Status: StatusPending, SenderAddress: "0x3", FunctionID: "f", WalletID: "w"},
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

	// Miss
	got2, err := s.GetByIdempotencyKey(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetByIdempotencyKey miss: %v", err)
	}
	if got2 != nil {
		t.Fatalf("expected nil, got %+v", got2)
	}

	// Empty key always returns nil
	got3, err := s.GetByIdempotencyKey(ctx, "")
	if err != nil {
		t.Fatalf("GetByIdempotencyKey empty: %v", err)
	}
	if got3 != nil {
		t.Fatalf("expected nil for empty key, got %+v", got3)
	}
}

func TestMemoryStore_Eviction(t *testing.T) {
	s := NewMemoryStore(50 * time.Millisecond)
	defer func(s *MemoryStore) {
		_ = s.Close()
	}(s)

	ctx := context.Background()
	rec := &TransactionRecord{
		ID:            "txn-evict",
		Status:        StatusPending,
		SenderAddress: "0xabc",
		FunctionID:    "0x1::module::func",
		WalletID:      "wallet-1",
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
