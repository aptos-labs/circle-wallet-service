package store

import (
	"context"
	"testing"
	"time"
)

// newPagedFixture seeds records with distinct UpdatedAt timestamps (and the
// status the caller requests) so tests can assert on the
// (updated_at ASC, id ASC) ordering semantics that ListByStatusPaged promises.
// Records are created in reverse time order to make sure the store isn't
// relying on insertion order.
func newPagedFixture(t *testing.T, s *MemoryStore, status TxnStatus, count int, base time.Time) []*TransactionRecord {
	return newPagedFixturePrefixed(t, s, "t", status, count, base)
}

func newPagedFixturePrefixed(t *testing.T, s *MemoryStore, prefix string, status TxnStatus, count int, base time.Time) []*TransactionRecord {
	t.Helper()
	ctx := context.Background()
	var recs []*TransactionRecord
	for i := count - 1; i >= 0; i-- {
		id := prefix + padNum(i)
		rec := &TransactionRecord{
			ID:            id,
			Status:        status,
			SenderAddress: "0x1",
			FunctionID:    "0x1::m::f",
			WalletID:      "w",
			PayloadJSON:   "{}",
		}
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
		// Overwrite UpdatedAt post-Create (Create sets it to now). Use minute
		// offsets so two records never share a timestamp unless we want that.
		got, _ := s.Get(ctx, id)
		got.UpdatedAt = base.Add(time.Duration(i) * time.Minute)
		if err := s.Update(ctx, got); err != nil {
			t.Fatalf("Update %s: %v", id, err)
		}
		// Update bumps UpdatedAt again; force it back.
		s.mu.Lock()
		s.records[id].UpdatedAt = base.Add(time.Duration(i) * time.Minute)
		s.mu.Unlock()
		recs = append(recs, s.records[id])
	}
	return recs
}

// padNum returns a 3-digit zero-padded number so lexicographic sort matches numeric.
func padNum(n int) string {
	if n < 10 {
		return "00" + string(rune('0'+n))
	}
	if n < 100 {
		return "0" + string(rune('0'+n/10)) + string(rune('0'+n%10))
	}
	return string(rune('0'+n/100)) + string(rune('0'+(n/10)%10)) + string(rune('0'+n%10))
}

// pad keeps backward-compat for existing callers that want the "t" prefix.
func pad(n int) string {
	return "t" + padNum(n)
}

func TestListByStatusPaged_OrderAndLimit(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func() { _ = s.Close() }()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	newPagedFixture(t, s, StatusSubmitted, 5, base)

	page, err := s.ListByStatusPaged(context.Background(), StatusSubmitted, 3, time.Time{}, "")
	if err != nil {
		t.Fatalf("paged: %v", err)
	}
	if len(page) != 3 {
		t.Fatalf("want 3 rows, got %d", len(page))
	}
	for i, rec := range page {
		wantID := pad(i)
		if rec.ID != wantID {
			t.Errorf("page[%d].ID = %q, want %q", i, rec.ID, wantID)
		}
		wantTime := base.Add(time.Duration(i) * time.Minute)
		if !rec.UpdatedAt.Equal(wantTime) {
			t.Errorf("page[%d].UpdatedAt = %v, want %v", i, rec.UpdatedAt, wantTime)
		}
	}
}

func TestListByStatusPaged_CursorAdvance(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func() { _ = s.Close() }()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	newPagedFixture(t, s, StatusSubmitted, 5, base)

	// Page through with limit 2: expect [0,1], [2,3], [4].
	cursorTime := time.Time{}
	cursorID := ""
	var seen []string
	for i := 0; i < 10; i++ {
		page, err := s.ListByStatusPaged(context.Background(), StatusSubmitted, 2, cursorTime, cursorID)
		if err != nil {
			t.Fatalf("paged: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, rec := range page {
			seen = append(seen, rec.ID)
		}
		cursorTime = page[len(page)-1].UpdatedAt
		cursorID = page[len(page)-1].ID
	}
	want := []string{"t000", "t001", "t002", "t003", "t004"}
	if len(seen) != len(want) {
		t.Fatalf("seen %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("seen[%d] = %q, want %q", i, seen[i], want[i])
		}
	}
}

func TestListByStatusPaged_SkipsOtherStatuses(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func() { _ = s.Close() }()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	newPagedFixturePrefixed(t, s, "sub-", StatusSubmitted, 3, base)
	// Seed rows in a different status with earlier timestamps — they should be
	// ignored even though they'd sort before the submitted rows.
	newPagedFixturePrefixed(t, s, "que-", StatusQueued, 3, base.Add(-time.Hour))

	page, err := s.ListByStatusPaged(context.Background(), StatusSubmitted, 10, time.Time{}, "")
	if err != nil {
		t.Fatalf("paged: %v", err)
	}
	if len(page) != 3 {
		t.Fatalf("want 3 submitted, got %d", len(page))
	}
	for _, rec := range page {
		if rec.Status != StatusSubmitted {
			t.Errorf("unexpected status %q in submitted page", rec.Status)
		}
	}
}

func TestListByStatusPaged_StrictGreaterOnTie(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	ts := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ids := []string{"a", "b", "c"}
	for _, id := range ids {
		rec := &TransactionRecord{
			ID: id, Status: StatusSubmitted, SenderAddress: "0x1",
			FunctionID: "f", WalletID: "w", PayloadJSON: "{}",
		}
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
		// Force every record to the same timestamp so the tie-breaker on id
		// becomes the only differentiator.
		s.mu.Lock()
		s.records[id].UpdatedAt = ts
		s.mu.Unlock()
	}

	// Passing "a" as the cursor id at the same timestamp must skip "a" and
	// return b, c — strict > on the (updated_at, id) tuple.
	page, err := s.ListByStatusPaged(ctx, StatusSubmitted, 10, ts, "a")
	if err != nil {
		t.Fatalf("paged: %v", err)
	}
	if len(page) != 2 || page[0].ID != "b" || page[1].ID != "c" {
		t.Fatalf("after cursor (ts,a): got %+v", idsOf(page))
	}

	// Cursor "b" should return only c.
	page, err = s.ListByStatusPaged(ctx, StatusSubmitted, 10, ts, "b")
	if err != nil {
		t.Fatalf("paged: %v", err)
	}
	if len(page) != 1 || page[0].ID != "c" {
		t.Fatalf("after cursor (ts,b): got %+v", idsOf(page))
	}
}

func idsOf(rs []*TransactionRecord) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

func TestPurgeTerminalOlderThan_FiltersAndCleansIndex(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)
	recs := []*TransactionRecord{
		{ID: "old-confirmed", Status: StatusConfirmed, IdempotencyKey: "k-oc"},
		{ID: "old-failed", Status: StatusFailed, IdempotencyKey: "k-of"},
		{ID: "old-expired", Status: StatusExpired},
		{ID: "old-queued", Status: StatusQueued, IdempotencyKey: "k-oq"},      // non-terminal: keep
		{ID: "recent-confirmed", Status: StatusConfirmed, IdempotencyKey: "k-rc"}, // too new: keep
	}
	for _, r := range recs {
		r.SenderAddress = "0x1"
		r.FunctionID = "f"
		r.WalletID = "w"
		r.PayloadJSON = "{}"
		if err := s.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// Backdate the "old-*" rows so they fall before the cutoff; the recent
	// row keeps its now-ish UpdatedAt.
	s.mu.Lock()
	for _, id := range []string{"old-confirmed", "old-failed", "old-expired"} {
		s.records[id].UpdatedAt = old
	}
	s.mu.Unlock()

	cutoff := now.Add(-time.Hour)
	removed, err := s.PurgeTerminalOlderThan(ctx, cutoff, 100)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if removed != 3 {
		t.Fatalf("removed=%d, want 3", removed)
	}
	// Surviving rows: old-queued (non-terminal), recent-confirmed (too new).
	for _, gone := range []string{"old-confirmed", "old-failed", "old-expired"} {
		got, _ := s.Get(ctx, gone)
		if got != nil {
			t.Errorf("%s should be purged, still present", gone)
		}
	}
	for _, kept := range []string{"old-queued", "recent-confirmed"} {
		got, _ := s.Get(ctx, kept)
		if got == nil {
			t.Errorf("%s should be kept, was purged", kept)
		}
	}
	// Idempotency index must not keep dangling pointers to deleted rows.
	for _, gone := range []string{"k-oc", "k-of"} {
		if got, _ := s.GetByIdempotencyKey(ctx, gone); got != nil {
			t.Errorf("idempotency index still points to purged row for key %s", gone)
		}
	}
	// And live rows' keys must still resolve.
	if got, _ := s.GetByIdempotencyKey(ctx, "k-oq"); got == nil {
		t.Error("non-terminal row's idempotency key should still resolve")
	}
	if got, _ := s.GetByIdempotencyKey(ctx, "k-rc"); got == nil {
		t.Error("recent terminal row's idempotency key should still resolve")
	}
}

func TestPurgeTerminalOlderThan_RespectsLimit(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)

	for i := 0; i < 10; i++ {
		rec := &TransactionRecord{
			ID: pad(i), Status: StatusConfirmed, SenderAddress: "0x1",
			FunctionID: "f", WalletID: "w", PayloadJSON: "{}",
		}
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		s.records[rec.ID].UpdatedAt = old
		s.mu.Unlock()
	}

	removed, err := s.PurgeTerminalOlderThan(ctx, now.Add(-time.Hour), 3)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if removed != 3 {
		t.Fatalf("removed=%d, want 3 (limit)", removed)
	}
	// 7 rows remain.
	remaining, _ := s.ListByStatus(ctx, StatusConfirmed)
	if len(remaining) != 7 {
		t.Fatalf("remaining=%d, want 7", len(remaining))
	}
}

func TestClearIdempotencyOlderThan_NullsKeyAndCleansIndex(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)

	recs := []*TransactionRecord{
		{ID: "old-terminal", Status: StatusConfirmed, IdempotencyKey: "k-old"},
		{ID: "old-non-terminal", Status: StatusSubmitted, IdempotencyKey: "k-old-sub"},
		{ID: "recent-terminal", Status: StatusConfirmed, IdempotencyKey: "k-recent"},
		{ID: "old-no-key", Status: StatusConfirmed},
	}
	for _, r := range recs {
		r.SenderAddress = "0x1"
		r.FunctionID = "f"
		r.WalletID = "w"
		r.PayloadJSON = "{}"
		if err := s.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	for _, id := range []string{"old-terminal", "old-non-terminal", "old-no-key"} {
		s.records[id].UpdatedAt = old
	}
	s.mu.Unlock()

	cleared, err := s.ClearIdempotencyOlderThan(ctx, now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("cleared=%d, want 1 (only old+terminal+keyed)", cleared)
	}
	// The old terminal row's key must now be empty and the index entry gone.
	got, _ := s.Get(ctx, "old-terminal")
	if got == nil || got.IdempotencyKey != "" {
		t.Errorf("old terminal row should have key cleared, got %+v", got)
	}
	if got, _ := s.GetByIdempotencyKey(ctx, "k-old"); got != nil {
		t.Error("idempotency index still points to the cleared row")
	}
	// Non-terminal row's key must be untouched (still indexed).
	if got, _ := s.GetByIdempotencyKey(ctx, "k-old-sub"); got == nil {
		t.Error("non-terminal row's key was cleared unexpectedly")
	}
	// Recent terminal row's key must be untouched.
	if got, _ := s.GetByIdempotencyKey(ctx, "k-recent"); got == nil {
		t.Error("recent terminal row's key was cleared unexpectedly")
	}
}

func TestClearIdempotencyOlderThan_RespectsLimit(t *testing.T) {
	t.Parallel()
	s := NewMemoryStore(time.Hour)
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)

	for i := 0; i < 5; i++ {
		rec := &TransactionRecord{
			ID: pad(i), Status: StatusConfirmed, IdempotencyKey: "k" + pad(i),
			SenderAddress: "0x1", FunctionID: "f", WalletID: "w", PayloadJSON: "{}",
		}
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		s.records[rec.ID].UpdatedAt = old
		s.mu.Unlock()
	}

	cleared, err := s.ClearIdempotencyOlderThan(ctx, now.Add(-time.Hour), 2)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared != 2 {
		t.Fatalf("cleared=%d, want 2 (limit)", cleared)
	}
	// Count rows that still have a key.
	var withKey int
	s.mu.Lock()
	for _, rec := range s.records {
		if rec.IdempotencyKey != "" {
			withKey++
		}
	}
	s.mu.Unlock()
	if withKey != 3 {
		t.Fatalf("withKey=%d, want 3", withKey)
	}
}
