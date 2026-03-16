package store

import (
	"context"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateAndGetTransaction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rec := &TransactionRecord{
		ID:             "test-id-1",
		OperationType:  "mint",
		Status:         StatusPending,
		Nonce:          12345,
		SenderAddress:  "0x1",
		MaxRetries:     3,
		RequestPayload: `{"to":"0x2","amount":100}`,
		ExpiresAt:      time.Now().UTC().Add(60 * time.Second),
	}

	if err := s.CreateTransaction(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetTransaction(ctx, "test-id-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected record, got nil")
	}
	if got.OperationType != "mint" {
		t.Errorf("operation_type = %q, want mint", got.OperationType)
	}
	if got.Status != StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got.Nonce != 12345 {
		t.Errorf("nonce = %d, want 12345", got.Nonce)
	}
}

func TestGetTransactionNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	got, err := s.GetTransaction(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestUpdateTransaction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rec := &TransactionRecord{
		ID:             "test-id-2",
		OperationType:  "burn",
		Status:         StatusPending,
		Nonce:          99999,
		SenderAddress:  "0x1",
		MaxRetries:     3,
		RequestPayload: "{}",
		ExpiresAt:      time.Now().UTC().Add(60 * time.Second),
	}
	if err := s.CreateTransaction(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}

	rec.Status = StatusSubmitted
	rec.TxnHash = "0xabcdef"
	if err := s.UpdateTransaction(ctx, rec); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := s.GetTransaction(ctx, "test-id-2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusSubmitted {
		t.Errorf("status = %q, want submitted", got.Status)
	}
	if got.TxnHash != "0xabcdef" {
		t.Errorf("txn_hash = %q, want 0xabcdef", got.TxnHash)
	}
}

func TestListByStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i, status := range []TxnStatus{StatusPending, StatusPending, StatusSubmitted} {
		rec := &TransactionRecord{
			ID:             "list-" + string(rune('a'+i)),
			OperationType:  "mint",
			Status:         status,
			SenderAddress:  "0x1",
			MaxRetries:     3,
			RequestPayload: "{}",
			ExpiresAt:      time.Now().UTC().Add(60 * time.Second),
		}
		if err := s.CreateTransaction(ctx, rec); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	pending, err := s.ListByStatus(ctx, StatusPending, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("pending count = %d, want 2", len(pending))
	}
}

func TestCreateTransaction_DuplicateID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rec := &TransactionRecord{
		ID: "dup-id", OperationType: "mint", Status: StatusPending,
		SenderAddress: "0x1", MaxRetries: 3, RequestPayload: "{}",
		ExpiresAt: time.Now().UTC().Add(60 * time.Second),
	}

	if err := s.CreateTransaction(ctx, rec); err != nil {
		t.Fatalf("first create: %v", err)
	}

	rec2 := &TransactionRecord{
		ID: "dup-id", OperationType: "burn", Status: StatusPending,
		SenderAddress: "0x2", MaxRetries: 3, RequestPayload: "{}",
		ExpiresAt: time.Now().UTC().Add(60 * time.Second),
	}
	err := s.CreateTransaction(ctx, rec2)
	if err == nil {
		t.Fatal("expected error for duplicate ID")
	}
}

func TestNewSQLiteStore_InvalidPath(t *testing.T) {
	// A path to a directory that doesn't exist should fail.
	_, err := NewSQLiteStore("/nonexistent/path/to/db.sqlite")
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

func TestOperationsAfterClose(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = s.Close()

	ctx := context.Background()

	// GetTransaction on a closed store should error.
	_, err = s.GetTransaction(ctx, "any-id")
	if err == nil {
		t.Error("expected error on GetTransaction after Close")
	}

	// CreateTransaction on a closed store should error.
	rec := &TransactionRecord{
		ID: "post-close", OperationType: "mint", Status: StatusPending,
		SenderAddress: "0x1", MaxRetries: 3, RequestPayload: "{}",
		ExpiresAt: time.Now().UTC(),
	}
	if err := s.CreateTransaction(ctx, rec); err == nil {
		t.Error("expected error on CreateTransaction after Close")
	}
}

func TestListByStatus_EmptyResult(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	records, err := s.ListByStatus(ctx, StatusConfirmed, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}

func TestListRetryable_NoRetryable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create only confirmed transactions — none should be retryable.
	rec := &TransactionRecord{
		ID: "confirmed-1", OperationType: "mint", Status: StatusConfirmed,
		SenderAddress: "0x1", MaxRetries: 3, RequestPayload: "{}",
		ExpiresAt: time.Now().UTC(),
	}
	if err := s.CreateTransaction(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}

	retryable, err := s.ListRetryable(ctx, 10)
	if err != nil {
		t.Fatalf("list retryable: %v", err)
	}
	if len(retryable) != 0 {
		t.Errorf("expected 0 retryable, got %d", len(retryable))
	}
}

func TestListByStatus_RespectsLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		rec := &TransactionRecord{
			ID: "limit-" + string(rune('a'+i)), OperationType: "mint",
			Status: StatusPending, SenderAddress: "0x1", MaxRetries: 3,
			RequestPayload: "{}", ExpiresAt: time.Now().UTC(),
		}
		if err := s.CreateTransaction(ctx, rec); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	records, err := s.ListByStatus(ctx, StatusPending, 3)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("expected 3 records (limited), got %d", len(records))
	}
}

func TestUpdateTransaction_SetsUpdatedAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rec := &TransactionRecord{
		ID: "ts-test", OperationType: "mint", Status: StatusPending,
		SenderAddress: "0x1", MaxRetries: 3, RequestPayload: "{}",
		ExpiresAt: time.Now().UTC().Add(60 * time.Second),
	}
	if err := s.CreateTransaction(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}

	originalUpdated := rec.UpdatedAt

	// Small delay to ensure time difference
	time.Sleep(10 * time.Millisecond)

	rec.Status = StatusSubmitted
	if err := s.UpdateTransaction(ctx, rec); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := s.GetTransaction(ctx, "ts-test")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.UpdatedAt.After(originalUpdated) {
		t.Error("UpdatedAt should advance after update")
	}
}

func TestListPendingRetries_RespectsRetryAfter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Pending with attempt > 0, retry_after in the past → should be returned
	rec1 := &TransactionRecord{
		ID: "pending-retry-1", OperationType: "mint", Status: StatusPending,
		SenderAddress: "0x1", Attempt: 1, MaxRetries: 3,
		RequestPayload: "{}", ExpiresAt: time.Now().UTC().Add(time.Hour),
		RetryAfter: time.Now().UTC().Add(-time.Minute),
	}
	// Pending with attempt > 0, retry_after in the future → should NOT be returned
	rec2 := &TransactionRecord{
		ID: "pending-retry-2", OperationType: "mint", Status: StatusPending,
		SenderAddress: "0x1", Attempt: 1, MaxRetries: 3,
		RequestPayload: "{}", ExpiresAt: time.Now().UTC().Add(time.Hour),
		RetryAfter: time.Now().UTC().Add(time.Hour),
	}
	// Pending with attempt == 0 → should NOT be returned (fresh submission)
	rec3 := &TransactionRecord{
		ID: "pending-fresh", OperationType: "mint", Status: StatusPending,
		SenderAddress: "0x1", Attempt: 0, MaxRetries: 3,
		RequestPayload: "{}", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}

	for _, r := range []*TransactionRecord{rec1, rec2, rec3} {
		if err := s.CreateTransaction(ctx, r); err != nil {
			t.Fatalf("create %s: %v", r.ID, err)
		}
	}

	results, err := s.ListPendingRetries(ctx, 10)
	if err != nil {
		t.Fatalf("list pending retries: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "pending-retry-1" {
		t.Errorf("expected pending-retry-1, got %s", results[0].ID)
	}
}

func TestRetryAfter_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	retryTime := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)

	rec := &TransactionRecord{
		ID: "retry-after-test", OperationType: "mint", Status: StatusPending,
		SenderAddress: "0x1", Attempt: 1, MaxRetries: 3,
		RequestPayload: "{}", ExpiresAt: time.Now().UTC().Add(time.Hour),
		RetryAfter: retryTime,
	}
	if err := s.CreateTransaction(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetTransaction(ctx, "retry-after-test")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Compare truncated times (SQLite datetime precision)
	gotTrunc := got.RetryAfter.Truncate(time.Second)
	if !gotTrunc.Equal(retryTime) {
		t.Errorf("RetryAfter = %v, want %v", gotTrunc, retryTime)
	}
}

func TestListRetryable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Retryable: failed with attempts remaining
	rec1 := &TransactionRecord{
		ID: "retry-1", OperationType: "mint", Status: StatusFailed,
		SenderAddress: "0x1", Attempt: 1, MaxRetries: 3,
		RequestPayload: "{}", ExpiresAt: time.Now().UTC(),
	}
	// Not retryable: max retries reached
	rec2 := &TransactionRecord{
		ID: "retry-2", OperationType: "mint", Status: StatusFailed,
		SenderAddress: "0x1", Attempt: 3, MaxRetries: 3,
		RequestPayload: "{}", ExpiresAt: time.Now().UTC(),
	}
	// Retryable: expired with attempts remaining
	rec3 := &TransactionRecord{
		ID: "retry-3", OperationType: "burn", Status: StatusExpired,
		SenderAddress: "0x1", Attempt: 0, MaxRetries: 3,
		RequestPayload: "{}", ExpiresAt: time.Now().UTC(),
	}

	for _, r := range []*TransactionRecord{rec1, rec2, rec3} {
		if err := s.CreateTransaction(ctx, r); err != nil {
			t.Fatalf("create %s: %v", r.ID, err)
		}
	}

	retryable, err := s.ListRetryable(ctx, 10)
	if err != nil {
		t.Fatalf("list retryable: %v", err)
	}
	if len(retryable) != 2 {
		t.Errorf("retryable count = %d, want 2", len(retryable))
	}
}
