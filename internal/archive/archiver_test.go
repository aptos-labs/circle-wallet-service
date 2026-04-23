package archive

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

// fakeStore implements just the two archive-facing methods plus the Store
// surface enough to compile. The other methods error so any accidental use
// surfaces loudly.
type fakeStore struct {
	mu               sync.Mutex
	purgeCalls       []fakePurgeCall
	idempotencyCalls []fakePurgeCall
	// purgeQueue / idempQueue feed sequential return values: when populated,
	// each call pops from the front. Empty means return 0.
	purgeQueue []int64
	idempQueue []int64
	purgeErr   error
	idempErr   error
}

type fakePurgeCall struct {
	cutoff time.Time
	limit  int
}

func (s *fakeStore) ClearIdempotencyOlderThan(_ context.Context, cutoff time.Time, limit int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idempotencyCalls = append(s.idempotencyCalls, fakePurgeCall{cutoff, limit})
	if s.idempErr != nil {
		return 0, s.idempErr
	}
	if len(s.idempQueue) == 0 {
		return 0, nil
	}
	n := s.idempQueue[0]
	s.idempQueue = s.idempQueue[1:]
	return n, nil
}

func (s *fakeStore) PurgeTerminalOlderThan(_ context.Context, cutoff time.Time, limit int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeCalls = append(s.purgeCalls, fakePurgeCall{cutoff, limit})
	if s.purgeErr != nil {
		return 0, s.purgeErr
	}
	if len(s.purgeQueue) == 0 {
		return 0, nil
	}
	n := s.purgeQueue[0]
	s.purgeQueue = s.purgeQueue[1:]
	return n, nil
}

// Unused Store methods — errors ensure tests that accidentally invoke them fail loudly.
var errNotUsed = errors.New("not used in archive tests")

func (s *fakeStore) Create(context.Context, *store.TransactionRecord) error { return errNotUsed }
func (s *fakeStore) Update(context.Context, *store.TransactionRecord) error { return errNotUsed }
func (s *fakeStore) UpdateIfStatus(context.Context, *store.TransactionRecord, store.TxnStatus) (bool, error) {
	return false, errNotUsed
}

func (s *fakeStore) Get(context.Context, string) (*store.TransactionRecord, error) {
	return nil, errNotUsed
}

func (s *fakeStore) GetByIdempotencyKey(context.Context, string) (*store.TransactionRecord, error) {
	return nil, errNotUsed
}

func (s *fakeStore) ListByStatus(context.Context, store.TxnStatus) ([]*store.TransactionRecord, error) {
	return nil, errNotUsed
}

func (s *fakeStore) ListByStatusPaged(context.Context, store.TxnStatus, int, time.Time, string) ([]*store.TransactionRecord, error) {
	return nil, errNotUsed
}
func (s *fakeStore) Close() error { return nil }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSweep_UsesExpectedCutoffs(t *testing.T) {
	fs := &fakeStore{}
	a := New(fs, Config{
		Tick:                 time.Minute,
		Retention:            30 * 24 * time.Hour,
		IdempotencyRetention: 7 * 24 * time.Hour,
		BatchSize:            500,
	}, quietLogger())

	start := time.Now().UTC()
	a.sweep(context.Background())

	if len(fs.idempotencyCalls) != 1 {
		t.Fatalf("want 1 idempotency call, got %d", len(fs.idempotencyCalls))
	}
	if len(fs.purgeCalls) != 1 {
		t.Fatalf("want 1 purge call, got %d", len(fs.purgeCalls))
	}

	gotIdempAge := start.Sub(fs.idempotencyCalls[0].cutoff)
	gotPurgeAge := start.Sub(fs.purgeCalls[0].cutoff)
	// Idempotency cutoff should sit ~7 days ago; purge cutoff ~30 days ago.
	if gotIdempAge < 6*24*time.Hour || gotIdempAge > 8*24*time.Hour {
		t.Errorf("idempotency cutoff age out of expected range: %v", gotIdempAge)
	}
	if gotPurgeAge < 29*24*time.Hour || gotPurgeAge > 31*24*time.Hour {
		t.Errorf("purge cutoff age out of expected range: %v", gotPurgeAge)
	}
	if fs.idempotencyCalls[0].limit != 500 || fs.purgeCalls[0].limit != 500 {
		t.Errorf("batch size not propagated: %+v / %+v", fs.idempotencyCalls[0], fs.purgeCalls[0])
	}
}

func TestSweep_LoopsUntilBatchShort(t *testing.T) {
	fs := &fakeStore{
		idempQueue: []int64{10, 10, 3},     // 3 calls, last is short
		purgeQueue: []int64{10, 10, 10, 0}, // 4 calls, last is short
	}
	a := New(fs, Config{
		Tick:                 time.Minute,
		Retention:            30 * 24 * time.Hour,
		IdempotencyRetention: 7 * 24 * time.Hour,
		BatchSize:            10,
	}, quietLogger())

	a.sweep(context.Background())

	if len(fs.idempotencyCalls) != 3 {
		t.Errorf("want 3 idempotency calls, got %d", len(fs.idempotencyCalls))
	}
	if len(fs.purgeCalls) != 4 {
		t.Errorf("want 4 purge calls, got %d", len(fs.purgeCalls))
	}
}

func TestSweep_StoreErrorStopsStage(t *testing.T) {
	fs := &fakeStore{
		idempErr: errors.New("boom"),
	}
	a := New(fs, Config{
		Tick:                 time.Minute,
		Retention:            30 * 24 * time.Hour,
		IdempotencyRetention: 7 * 24 * time.Hour,
		BatchSize:            10,
	}, quietLogger())

	a.sweep(context.Background())

	// idempotency stage errors out after one call; purge stage still runs.
	if len(fs.idempotencyCalls) != 1 {
		t.Errorf("want 1 idempotency call, got %d", len(fs.idempotencyCalls))
	}
	if len(fs.purgeCalls) != 1 {
		t.Errorf("want 1 purge call, got %d", len(fs.purgeCalls))
	}
}

func TestSweep_ContextCancelAborts(t *testing.T) {
	fs := &fakeStore{
		// Never returns a short batch, so the loop would run forever without cancellation.
		idempQueue: make([]int64, 1000),
	}
	for i := range fs.idempQueue {
		fs.idempQueue[i] = 10
	}
	a := New(fs, Config{
		Tick:                 time.Minute,
		Retention:            30 * 24 * time.Hour,
		IdempotencyRetention: 7 * 24 * time.Hour,
		BatchSize:            10,
	}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before sweep starts

	a.sweep(ctx)
	// Because ctx is cancelled, neither stage should do any calls
	// (each stage's first iteration checks ctx.Err before calling the store).
	if len(fs.idempotencyCalls) != 0 {
		t.Errorf("want 0 idempotency calls after pre-cancelled ctx, got %d", len(fs.idempotencyCalls))
	}
	if len(fs.purgeCalls) != 0 {
		t.Errorf("want 0 purge calls after pre-cancelled ctx, got %d", len(fs.purgeCalls))
	}
}

// TestArchiverAgainstMemoryStore wires the Archiver against the real
// MemoryStore implementation so we catch anything that slips past the
// fakeStore (interface drift, wrong argument order, cursor bugs, etc.).
//
// Strategy: we can't backdate a row via MemoryStore's public API (Create and
// Update both stamp UpdatedAt=now). Instead we create rows at "now", sleep
// past a small retention window, then configure the Archiver with millisecond
// retention so everything we seeded now qualifies as aged.
func TestArchiverAgainstMemoryStore(t *testing.T) {
	ms := store.NewMemoryStore(time.Hour)
	defer func() { _ = ms.Close() }()
	ctx := context.Background()

	mk := func(id string, status store.TxnStatus, key string) {
		t.Helper()
		rec := &store.TransactionRecord{
			ID: id, Status: status, IdempotencyKey: key,
			SenderAddress: "0x1", FunctionID: "f", WalletID: "w", PayloadJSON: "{}",
		}
		if err := ms.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	mk("fresh-a", store.StatusConfirmed, "ka")
	mk("fresh-b", store.StatusConfirmed, "kb")
	mk("non-terminal", store.StatusSubmitted, "kc")
	mk("no-key", store.StatusFailed, "")

	// Sleep so all seeded rows age past the tiny retention windows below.
	time.Sleep(25 * time.Millisecond)

	a := New(ms, Config{
		Tick:                 time.Hour,
		Retention:            10 * time.Millisecond,
		IdempotencyRetention: 5 * time.Millisecond,
		BatchSize:            100,
	}, quietLogger())

	a.sweep(ctx)

	// Terminal rows (fresh-a, fresh-b, no-key) must be purged. Non-terminal stays.
	for _, gone := range []string{"fresh-a", "fresh-b", "no-key"} {
		got, _ := ms.Get(ctx, gone)
		if got != nil {
			t.Errorf("%s should have been purged; still present: %+v", gone, got)
		}
	}
	got, _ := ms.Get(ctx, "non-terminal")
	if got == nil {
		t.Error("non-terminal row was wrongly purged")
	} else if got.IdempotencyKey != "kc" {
		t.Errorf("non-terminal row key was wrongly cleared: %+v", got)
	}
	// Idempotency index must not retain pointers to purged rows.
	for _, k := range []string{"ka", "kb"} {
		if got, _ := ms.GetByIdempotencyKey(ctx, k); got != nil {
			t.Errorf("index still points to purged row for key %s", k)
		}
	}
}

func TestNew_DefaultsApplied(t *testing.T) {
	fs := &fakeStore{}
	a := New(fs, Config{}, quietLogger())
	if a.tick != 5*time.Minute {
		t.Errorf("default tick not applied: %v", a.tick)
	}
	if a.retention != 30*24*time.Hour {
		t.Errorf("default retention not applied: %v", a.retention)
	}
	if a.idempotencyRetention != 7*24*time.Hour {
		t.Errorf("default idempotency retention not applied: %v", a.idempotencyRetention)
	}
	if a.batchSize != 1000 {
		t.Errorf("default batch size not applied: %d", a.batchSize)
	}
}
