package store

import (
	"context"
	"testing"
	"time"
)

// newQueueStore returns a MemoryStore ready for Queue tests.
func newQueueStore(t *testing.T) *MemoryStore {
	t.Helper()
	s := NewMemoryStore(time.Hour)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedQueued inserts a queued record with a deterministic CreatedAt so
// ordering tests remain stable.
func seedQueued(t *testing.T, s *MemoryStore, id, sender string, createdAt time.Time) {
	t.Helper()
	rec := &TransactionRecord{
		ID:            id,
		Status:        StatusQueued,
		SenderAddress: sender,
		FunctionID:    "0x1::m::f",
		WalletID:      "w",
	}
	if err := s.Create(context.Background(), rec); err != nil {
		t.Fatalf("Create %s: %v", id, err)
	}
	s.mu.Lock()
	s.records[id].CreatedAt = createdAt
	s.records[id].UpdatedAt = createdAt
	s.mu.Unlock()
}

func TestMemoryQueue_ClaimNextAllocatesSequence(t *testing.T) {
	t.Parallel()
	s := newQueueStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Minute)
	seedQueued(t, s, "a", "0xs", base)
	seedQueued(t, s, "b", "0xs", base.Add(10*time.Second))

	first, err := s.ClaimNextQueuedForSender(ctx, "0xs")
	if err != nil || first == nil {
		t.Fatalf("claim 1: %v rec=%v", err, first)
	}
	if first.ID != "a" {
		t.Fatalf("expected oldest (a), got %s", first.ID)
	}
	if first.Status != StatusProcessing {
		t.Fatalf("status = %q, want processing", first.Status)
	}
	if first.SequenceNumber == nil || *first.SequenceNumber != 0 {
		t.Fatalf("seq = %v, want 0", first.SequenceNumber)
	}

	second, err := s.ClaimNextQueuedForSender(ctx, "0xs")
	if err != nil || second == nil {
		t.Fatalf("claim 2: %v rec=%v", err, second)
	}
	if second.ID != "b" {
		t.Fatalf("expected b, got %s", second.ID)
	}
	if second.SequenceNumber == nil || *second.SequenceNumber != 1 {
		t.Fatalf("seq = %v, want 1", second.SequenceNumber)
	}

	none, err := s.ClaimNextQueuedForSender(ctx, "0xs")
	if err != nil || none != nil {
		t.Fatalf("expected nil, nil; got %v %v", none, err)
	}
}

func TestMemoryQueue_ListQueuedSendersOrderedByOldest(t *testing.T) {
	t.Parallel()
	s := newQueueStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	// Sender A has an old queued record, B has a newer one.
	seedQueued(t, s, "a1", "0xA", base)
	seedQueued(t, s, "b1", "0xB", base.Add(30*time.Minute))
	seedQueued(t, s, "a2", "0xA", base.Add(45*time.Minute))

	got, err := s.ListQueuedSenders(ctx)
	if err != nil {
		t.Fatalf("ListQueuedSenders: %v", err)
	}
	if len(got) != 2 || got[0] != "0xA" || got[1] != "0xB" {
		t.Fatalf("got %v, want [0xA 0xB]", got)
	}
}

func TestMemoryQueue_ReconcileIsUpOnly(t *testing.T) {
	t.Parallel()
	s := newQueueStore(t)
	ctx := context.Background()
	// Start by allocating a sequence to bump the counter to 1.
	seedQueued(t, s, "a", "0xs", time.Now())
	if _, err := s.ClaimNextQueuedForSender(ctx, "0xs"); err != nil {
		t.Fatal(err)
	}
	// counter == 1 now. Reconciling with a lower value is a no-op.
	if err := s.ReconcileSequence(ctx, "0xs", 0); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	got := s.sequences["0xs"]
	s.mu.RUnlock()
	if got != 1 {
		t.Fatalf("counter = %d, want 1 (down-reconcile should be a no-op)", got)
	}
	// Reconciling with a higher value raises.
	if err := s.ReconcileSequence(ctx, "0xs", 5); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	got = s.sequences["0xs"]
	s.mu.RUnlock()
	if got != 5 {
		t.Fatalf("counter = %d, want 5", got)
	}
}

func TestMemoryQueue_ForceResetIncludesInflight(t *testing.T) {
	t.Parallel()
	s := newQueueStore(t)
	ctx := context.Background()
	// Two submitted rows at seq 10 and 11, and one queued row. Counter was 12.
	for i, id := range []string{"s10", "s11"} {
		seq := uint64(10 + i)
		rec := &TransactionRecord{
			ID:             id,
			Status:         StatusSubmitted,
			SenderAddress:  "0xs",
			SequenceNumber: &seq,
			FunctionID:     "0x1::m::f",
			WalletID:       "w",
		}
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	s.sequences["0xs"] = 12
	s.mu.Unlock()

	// Chain says last seq is 9 (so next chain seq == 10). We have 2 inflight at ≥10.
	if err := s.ForceResetSequenceToChain(ctx, "0xs", 10); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	got := s.sequences["0xs"]
	s.mu.RUnlock()
	if got != 12 { // 10 + 2 inflight
		t.Fatalf("counter = %d, want 12 (chainSeq 10 + 2 inflight)", got)
	}
}

func TestMemoryQueue_RecoverStaleProcessingSkipsHashed(t *testing.T) {
	t.Parallel()
	s := newQueueStore(t)
	ctx := context.Background()

	old := time.Now().Add(-10 * time.Minute)
	seq0, seq1 := uint64(0), uint64(1)
	// Hashed row — must be left alone (owned by poller recovery path).
	hashed := &TransactionRecord{
		ID:             "hashed",
		Status:         StatusProcessing,
		SenderAddress:  "0xs",
		SequenceNumber: &seq0,
		TxnHash:        "0xdeadbeef",
		FunctionID:     "0x1::m::f",
		WalletID:       "w",
	}
	stuck := &TransactionRecord{
		ID:             "stuck",
		Status:         StatusProcessing,
		SenderAddress:  "0xs",
		SequenceNumber: &seq1,
		FunctionID:     "0x1::m::f",
		WalletID:       "w",
	}
	if err := s.Create(ctx, hashed); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, stuck); err != nil {
		t.Fatal(err)
	}
	// Age them past the cutoff.
	s.mu.Lock()
	s.records["hashed"].UpdatedAt = old
	s.records["stuck"].UpdatedAt = old
	s.sequences["0xs"] = 2
	s.mu.Unlock()

	n, err := s.RecoverStaleProcessing(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("recovered = %d, want 1 (hashed row must be skipped)", n)
	}
	got, _ := s.Get(ctx, "hashed")
	if got.Status != StatusProcessing || got.TxnHash == "" {
		t.Fatalf("hashed row was mutated: %+v", got)
	}
	got, _ = s.Get(ctx, "stuck")
	if got.Status != StatusQueued || got.SequenceNumber != nil {
		t.Fatalf("stuck row not re-queued: %+v", got)
	}
	s.mu.RLock()
	if s.sequences["0xs"] != 1 {
		t.Fatalf("counter = %d, want 1 (2 - 1 recovered)", s.sequences["0xs"])
	}
	s.mu.RUnlock()
}

func TestMemoryQueue_ShiftSenderSequences(t *testing.T) {
	t.Parallel()
	s := newQueueStore(t)
	ctx := context.Background()
	seqs := []uint64{3, 4, 5}
	for i, sq := range seqs {
		status := StatusQueued
		if i == 0 {
			status = StatusProcessing
		}
		rec := &TransactionRecord{
			ID:             string(rune('a' + i)),
			Status:         status,
			SenderAddress:  "0xs",
			SequenceNumber: &sq,
			FunctionID:     "0x1::m::f",
			WalletID:       "w",
		}
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	s.sequences["0xs"] = 6
	s.mu.Unlock()

	// Fail at seq 3 — shift 4 and 5 back to queued.
	if err := s.ShiftSenderSequences(ctx, "0xs", 3); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]TxnStatus{
		"a": StatusProcessing, // seq 3 (== failed), untouched
		"b": StatusQueued,     // seq 4, reset
		"c": StatusQueued,     // seq 5, reset
	} {
		got, _ := s.Get(ctx, id)
		if got.Status != want {
			t.Errorf("%s: status = %q, want %q", id, got.Status, want)
		}
	}
	s.mu.RLock()
	if s.sequences["0xs"] != 4 {
		t.Fatalf("counter = %d, want 4 (6 - 2)", s.sequences["0xs"])
	}
	s.mu.RUnlock()
}

func TestMemoryQueue_ReleaseSequenceBoundedAtZero(t *testing.T) {
	t.Parallel()
	s := newQueueStore(t)
	ctx := context.Background()
	if err := s.ReleaseSequence(ctx, "0xs"); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	if s.sequences["0xs"] != 0 {
		t.Fatalf("counter went negative: %d", s.sequences["0xs"])
	}
	s.mu.RUnlock()
}
