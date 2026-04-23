package poller

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aptos-labs/aptos-go-sdk/api"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

type mockTxnFetcher struct {
	mu    sync.Mutex
	fn    func(hash string) (*api.Transaction, error)
	calls int
}

func (m *mockTxnFetcher) TransactionByHashCtx(ctx context.Context, hash string) (*api.Transaction, error) {
	m.mu.Lock()
	m.calls++
	fn := m.fn
	m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fn != nil {
		return fn(hash)
	}
	return nil, errors.New("no mock")
}

type mockNotifier struct {
	mu    sync.Mutex
	count int
}

func (m *mockNotifier) Notify(_ context.Context, _ *store.TransactionRecord) {
	m.mu.Lock()
	m.count++
	m.mu.Unlock()
}

func (m *mockNotifier) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

type stubStore struct {
	list          []*store.TransactionRecord
	listErr       error
	updateIfCalls int
	updateIfFn    func(ctx context.Context, rec *store.TransactionRecord, expected store.TxnStatus) (bool, error)
}

func (s *stubStore) Create(context.Context, *store.TransactionRecord) error {
	return errors.New("stub")
}

func (s *stubStore) Update(context.Context, *store.TransactionRecord) error {
	return errors.New("stub")
}
func (s *stubStore) Get(context.Context, string) (*store.TransactionRecord, error) { return nil, nil }
func (s *stubStore) GetByIdempotencyKey(context.Context, string) (*store.TransactionRecord, error) {
	return nil, nil
}
func (s *stubStore) Close() error { return nil }

func (s *stubStore) ListByStatus(_ context.Context, _ store.TxnStatus) ([]*store.TransactionRecord, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.list, nil
}

// ListByStatusPaged delegates to the full list here; test fixtures are small
// enough that paging offers no signal worth mimicking.
func (s *stubStore) ListByStatusPaged(ctx context.Context, status store.TxnStatus, _ int, _ time.Time, _ string) ([]*store.TransactionRecord, error) {
	return s.ListByStatus(ctx, status)
}

func (s *stubStore) PurgeTerminalOlderThan(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (s *stubStore) ClearIdempotencyOlderThan(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (s *stubStore) UpdateIfStatus(ctx context.Context, rec *store.TransactionRecord, expected store.TxnStatus) (bool, error) {
	s.updateIfCalls++
	if s.updateIfFn != nil {
		return s.updateIfFn(ctx, rec, expected)
	}
	return false, nil
}

func userTxn(success bool, vmStatus string) *api.Transaction {
	return &api.Transaction{
		Type: api.TransactionVariantUser,
		Inner: &api.UserTransaction{
			Success:  success,
			VmStatus: vmStatus,
		},
	}
}

func TestPoller_ConfirmedTransaction(t *testing.T) {
	st := store.NewMemoryStore(time.Hour)
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	rec := &store.TransactionRecord{
		ID:            "t1",
		Status:        store.StatusSubmitted,
		SenderAddress: "0x1",
		FunctionID:    "0x1::m::f",
		WalletID:      "w",
		TxnHash:       "0xabc",
		CreatedAt:     now,
		UpdatedAt:     now,
		ExpiresAt:     now.Add(time.Hour),
	}
	if err := st.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	fetch := &mockTxnFetcher{fn: func(string) (*api.Transaction, error) {
		return userTxn(true, ""), nil
	}}
	n := &mockNotifier{}
	p := &Poller{
		client:   fetch,
		store:    st,
		notifier: n,
		interval: time.Minute,
		logger:   slog.New(slog.DiscardHandler),
	}
	p.poll(context.Background())
	if n.calls() != 1 {
		t.Fatalf("notifier calls=%d", n.calls())
	}
	got, _ := st.Get(context.Background(), "t1")
	if got == nil || got.Status != store.StatusConfirmed {
		t.Fatalf("status=%v", got)
	}
}

func TestPoller_FailedTransaction(t *testing.T) {
	st := store.NewMemoryStore(time.Hour)
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	rec := &store.TransactionRecord{
		ID:            "t2",
		Status:        store.StatusSubmitted,
		SenderAddress: "0x1",
		FunctionID:    "0x1::m::f",
		WalletID:      "w",
		TxnHash:       "0xdef",
		CreatedAt:     now,
		UpdatedAt:     now,
		ExpiresAt:     now.Add(time.Hour),
	}
	if err := st.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	fetch := &mockTxnFetcher{fn: func(string) (*api.Transaction, error) {
		return userTxn(false, "move_abort"), nil
	}}
	n := &mockNotifier{}
	p := &Poller{
		client:   fetch,
		store:    st,
		notifier: n,
		interval: time.Minute,
		logger:   slog.New(slog.DiscardHandler),
	}
	p.poll(context.Background())
	if n.calls() != 1 {
		t.Fatalf("notifier calls=%d", n.calls())
	}
	got, _ := st.Get(context.Background(), "t2")
	if got == nil || got.Status != store.StatusFailed || got.ErrorMessage != "move_abort" {
		t.Fatalf("record %#v", got)
	}
}

func TestPoller_PendingTransaction(t *testing.T) {
	st := store.NewMemoryStore(time.Hour)
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	rec := &store.TransactionRecord{
		ID:            "t3",
		Status:        store.StatusSubmitted,
		SenderAddress: "0x1",
		FunctionID:    "0x1::m::f",
		WalletID:      "w",
		TxnHash:       "0xpending",
		CreatedAt:     now,
		UpdatedAt:     now,
		ExpiresAt:     now.Add(time.Hour),
	}
	if err := st.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	fetch := &mockTxnFetcher{fn: func(string) (*api.Transaction, error) {
		return &api.Transaction{
			Type:  api.TransactionVariantPending,
			Inner: &api.PendingTransaction{},
		}, nil
	}}
	n := &mockNotifier{}
	p := &Poller{
		client:   fetch,
		store:    st,
		notifier: n,
		interval: time.Minute,
		logger:   slog.New(slog.DiscardHandler),
	}
	p.poll(context.Background())
	if n.calls() != 0 {
		t.Fatalf("notifier calls=%d", n.calls())
	}
	got, _ := st.Get(context.Background(), "t3")
	if got == nil {
		t.Fatalf("record %#v", got)
	} else if got.Status != store.StatusSubmitted {
		t.Fatalf("status=%v", got.Status)
	}
}

func TestPoller_ExpiredTransaction(t *testing.T) {
	st := store.NewMemoryStore(time.Hour)
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	rec := &store.TransactionRecord{
		ID:            "t4",
		Status:        store.StatusSubmitted,
		SenderAddress: "0x1",
		FunctionID:    "0x1::m::f",
		WalletID:      "w",
		TxnHash:       "0xhash",
		CreatedAt:     now.Add(-2 * time.Hour),
		UpdatedAt:     now.Add(-2 * time.Hour),
		ExpiresAt:     now.Add(-time.Minute),
	}
	if err := st.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	fetch := &mockTxnFetcher{}
	n := &mockNotifier{}
	p := &Poller{
		client:   fetch,
		store:    st,
		notifier: n,
		interval: time.Minute,
		logger:   slog.New(slog.DiscardHandler),
	}
	p.poll(context.Background())
	if fetch.calls != 1 {
		t.Fatalf("fetcher calls=%d, want 1 (on-chain check before marking expired)", fetch.calls)
	}
	if n.calls() != 1 {
		t.Fatalf("notifier calls=%d", n.calls())
	}
	got, _ := st.Get(context.Background(), "t4")
	if got == nil || got.Status != store.StatusExpired {
		t.Fatalf("status=%v", got.Status)
	}
}

func TestPoller_ConditionalUpdateSkipsDuplicate(t *testing.T) {
	now := time.Now().UTC()
	rec := &store.TransactionRecord{
		ID:            "t5",
		Status:        store.StatusSubmitted,
		SenderAddress: "0x1",
		FunctionID:    "0x1::m::f",
		WalletID:      "w",
		TxnHash:       "0xdup",
		CreatedAt:     now,
		UpdatedAt:     now,
		ExpiresAt:     now.Add(time.Hour),
	}
	st := &stubStore{
		list: []*store.TransactionRecord{rec},
		updateIfFn: func(context.Context, *store.TransactionRecord, store.TxnStatus) (bool, error) {
			return false, nil
		},
	}
	fetch := &mockTxnFetcher{fn: func(string) (*api.Transaction, error) {
		return userTxn(true, ""), nil
	}}
	n := &mockNotifier{}
	p := &Poller{
		client:   fetch,
		store:    st,
		notifier: n,
		interval: time.Minute,
		logger:   slog.New(slog.DiscardHandler),
	}
	p.poll(context.Background())
	if n.calls() != 0 {
		t.Fatalf("notifier calls=%d", n.calls())
	}
	if st.updateIfCalls < 1 {
		t.Fatalf("UpdateIfStatus not called")
	}
}

func TestPoller_NoHash(t *testing.T) {
	st := store.NewMemoryStore(time.Hour)
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	rec := &store.TransactionRecord{
		ID:            "t6",
		Status:        store.StatusSubmitted,
		SenderAddress: "0x1",
		FunctionID:    "0x1::m::f",
		WalletID:      "w",
		TxnHash:       "",
		CreatedAt:     now,
		UpdatedAt:     now,
		ExpiresAt:     now.Add(time.Hour),
	}
	if err := st.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	fetch := &mockTxnFetcher{}
	n := &mockNotifier{}
	p := &Poller{
		client:   fetch,
		store:    st,
		notifier: n,
		interval: time.Minute,
		logger:   slog.New(slog.DiscardHandler),
	}
	p.poll(context.Background())
	if fetch.calls != 0 {
		t.Fatalf("fetcher calls=%d", fetch.calls)
	}
	if n.calls() != 0 {
		t.Fatalf("notifier calls=%d", n.calls())
	}
}

func TestPollLoopExitsOnCancel(t *testing.T) {
	st := store.NewMemoryStore(time.Hour)
	defer func() { _ = st.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := New(&mockTxnFetcher{}, st, &mockNotifier{}, time.Millisecond, 0, 0, 0, 0, slog.New(slog.DiscardHandler))
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit")
	}
}

func TestMultipleSubmittedTransactions(t *testing.T) {
	st := store.NewMemoryStore(time.Hour)
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	exp := now.Add(time.Hour)
	records := []*store.TransactionRecord{
		{ID: "m1", Status: store.StatusSubmitted, SenderAddress: "0x1", FunctionID: "0x1::m::f", WalletID: "w", TxnHash: "0xh1", CreatedAt: now, UpdatedAt: now, ExpiresAt: exp},
		{ID: "m2", Status: store.StatusSubmitted, SenderAddress: "0x1", FunctionID: "0x1::m::f", WalletID: "w", TxnHash: "0xh2", CreatedAt: now, UpdatedAt: now, ExpiresAt: exp},
		{ID: "m3", Status: store.StatusSubmitted, SenderAddress: "0x1", FunctionID: "0x1::m::f", WalletID: "w", TxnHash: "0xh3", CreatedAt: now, UpdatedAt: now, ExpiresAt: exp},
	}
	for _, rec := range records {
		if err := st.Create(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	fetch := &mockTxnFetcher{fn: func(hash string) (*api.Transaction, error) {
		switch hash {
		case "0xh1":
			return userTxn(true, ""), nil
		case "0xh2":
			return userTxn(false, "abort"), nil
		default:
			return &api.Transaction{
				Type:  api.TransactionVariantPending,
				Inner: &api.PendingTransaction{},
			}, nil
		}
	}}
	n := &mockNotifier{}
	p := &Poller{
		client:   fetch,
		store:    st,
		notifier: n,
		interval: time.Minute,
		logger:   slog.New(slog.DiscardHandler),
	}
	p.poll(context.Background())
	if n.calls() != 2 {
		t.Fatalf("notifier calls=%d want 2", n.calls())
	}
	s1, _ := st.Get(context.Background(), "m1")
	s2, _ := st.Get(context.Background(), "m2")
	s3, _ := st.Get(context.Background(), "m3")
	if s1 == nil || s1.Status != store.StatusConfirmed {
		t.Fatalf("m1: %#v", s1)
	}
	if s2 == nil || s2.Status != store.StatusFailed {
		t.Fatalf("m2: %#v", s2)
	}
	if s3 == nil || s3.Status != store.StatusSubmitted {
		t.Fatalf("m3: %#v", s3)
	}
}

func TestTransactionHashEmpty(t *testing.T) {
	st := store.NewMemoryStore(time.Hour)
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	rec := &store.TransactionRecord{
		ID: "empty-hash", Status: store.StatusSubmitted, SenderAddress: "0x1", FunctionID: "0x1::m::f",
		WalletID: "w", TxnHash: "", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := st.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	fetch := &mockTxnFetcher{}
	n := &mockNotifier{}
	p := &Poller{
		client: fetch, store: st, notifier: n, interval: time.Minute, logger: slog.New(slog.DiscardHandler),
	}
	p.poll(context.Background())
	if fetch.calls != 0 {
		t.Fatalf("fetcher calls=%d", fetch.calls)
	}
	if n.calls() != 0 {
		t.Fatalf("notifier calls=%d", n.calls())
	}
}

func TestAPIErrorDoesNotCrash(t *testing.T) {
	st := store.NewMemoryStore(time.Hour)
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	exp := now.Add(time.Hour)
	for _, id := range []string{"e1", "e2"} {
		rec := &store.TransactionRecord{
			ID: id, Status: store.StatusSubmitted, SenderAddress: "0x1", FunctionID: "0x1::m::f",
			WalletID: "w", TxnHash: "0x" + id, CreatedAt: now, UpdatedAt: now, ExpiresAt: exp,
		}
		if err := st.Create(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	var calls int
	fetch := &mockTxnFetcher{fn: func(hash string) (*api.Transaction, error) {
		calls++
		if hash == "0xe1" {
			return nil, errors.New("rpc down")
		}
		return userTxn(true, ""), nil
	}}
	n := &mockNotifier{}
	p := &Poller{
		client: fetch, store: st, notifier: n, interval: time.Minute, logger: slog.New(slog.DiscardHandler),
	}
	p.poll(context.Background())
	if calls < 2 {
		t.Fatalf("fetcher calls=%d want >=2", calls)
	}
	if n.calls() != 1 {
		t.Fatalf("notifier calls=%d want 1", n.calls())
	}
	got, _ := st.Get(context.Background(), "e1")
	if got == nil || got.Status != store.StatusSubmitted {
		t.Fatalf("e1 after error: %#v", got)
	}
}

func TestVmStatusNonUserInner(t *testing.T) {
	got := vmStatus(&api.Transaction{Inner: nil})
	if got != "unknown vm_status" {
		t.Fatalf("got %q", got)
	}
}

// TestPollerRateLimiterThrottles seeds more submitted rows than the limiter's
// burst allows, runs one poll(), and asserts the actual wall-clock elapsed
// matches the rate budget. 2 RPS burst=2 + 5 rows ⇒ 3 waits at 500ms each ⇒
// at least ~1.5s before poll() returns.
func TestPollerRateLimiterThrottles(t *testing.T) {
	st := store.NewMemoryStore(time.Hour)
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	exp := now.Add(time.Hour)
	for i := range 5 {
		rec := &store.TransactionRecord{
			ID: "rl-" + string(rune('a'+i)), Status: store.StatusSubmitted,
			SenderAddress: "0x1", FunctionID: "0x1::m::f", WalletID: "w",
			TxnHash: "0xh" + string(rune('a'+i)), CreatedAt: now, UpdatedAt: now, ExpiresAt: exp,
		}
		if err := st.Create(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	// Fetcher returns a pending txn (Success()==nil) so poll doesn't mutate.
	fetch := &mockTxnFetcher{fn: func(string) (*api.Transaction, error) {
		return &api.Transaction{Inner: &api.PendingTransaction{}}, nil
	}}
	p := New(fetch, st, &mockNotifier{}, time.Minute, 2, 2, 0, 0, slog.New(slog.DiscardHandler))

	start := time.Now()
	p.poll(context.Background())
	elapsed := time.Since(start)

	fetch.mu.Lock()
	calls := fetch.calls
	fetch.mu.Unlock()
	if calls != 5 {
		t.Fatalf("expected 5 fetcher calls, got %d", calls)
	}
	// 5 calls at 2 RPS with burst 2: 2 immediate + 3 waits at 500ms each = ~1.5s.
	// Allow a slack floor of 1.2s to avoid flakes on slow CI.
	if elapsed < 1200*time.Millisecond {
		t.Fatalf("expected throttling to take >=1.2s, got %v", elapsed)
	}
}

// pagingStubStore honors pageSize + cursor so we can verify that the poller
// actually paginates (the simpler stubStore flattens paging into one list).
// It records the (limit, cursorTime, cursorID) of every ListByStatusPaged call
// so tests can assert the cursor advances page-by-page.
type pagingStubStore struct {
	mu      sync.Mutex
	records []*store.TransactionRecord // assumed pre-sorted by (UpdatedAt, ID) ASC
	calls   []pageCall
}

type pageCall struct {
	limit      int
	cursorTime time.Time
	cursorID   string
	returned   int
}

func (s *pagingStubStore) Create(context.Context, *store.TransactionRecord) error {
	return errors.New("stub")
}

func (s *pagingStubStore) Update(context.Context, *store.TransactionRecord) error {
	return errors.New("stub")
}

func (s *pagingStubStore) Get(context.Context, string) (*store.TransactionRecord, error) {
	return nil, nil
}

func (s *pagingStubStore) GetByIdempotencyKey(context.Context, string) (*store.TransactionRecord, error) {
	return nil, nil
}
func (s *pagingStubStore) Close() error { return nil }
func (s *pagingStubStore) PurgeTerminalOlderThan(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (s *pagingStubStore) ClearIdempotencyOlderThan(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (s *pagingStubStore) UpdateIfStatus(context.Context, *store.TransactionRecord, store.TxnStatus) (bool, error) {
	return false, nil
}

func (s *pagingStubStore) ListByStatus(_ context.Context, _ store.TxnStatus) ([]*store.TransactionRecord, error) {
	return nil, errors.New("stub: use ListByStatusPaged")
}

func (s *pagingStubStore) ListByStatusPaged(_ context.Context, status store.TxnStatus, limit int, cursorTime time.Time, cursorID string) ([]*store.TransactionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := pageCall{limit: limit, cursorTime: cursorTime, cursorID: cursorID}
	out := make([]*store.TransactionRecord, 0, limit)
	for _, r := range s.records {
		if r.Status != status {
			continue
		}
		// Strict (updated_at, id) > (cursor) on the zero cursor means return
		// everything, which matches the real MySQL implementation.
		if !cursorTime.IsZero() {
			if r.UpdatedAt.Before(cursorTime) {
				continue
			}
			if r.UpdatedAt.Equal(cursorTime) && r.ID <= cursorID {
				continue
			}
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	call.returned = len(out)
	s.calls = append(s.calls, call)
	return out, nil
}

// TestPollerPaginatesAcrossPages seeds more rows than one page and verifies
// that the poller walks through all of them by advancing the cursor.
// Also asserts the sweep stops after a short page rather than issuing
// an extra zero-row query.
func TestPollerPaginatesAcrossPages(t *testing.T) {
	now := time.Now().UTC()
	exp := now.Add(time.Hour)
	// 7 rows total, page size 3 ⇒ expect pages of sizes 3, 3, 1 (short ⇒ stop).
	var records []*store.TransactionRecord
	for i := range 7 {
		records = append(records, &store.TransactionRecord{
			ID: "pg-" + string(rune('a'+i)), Status: store.StatusSubmitted,
			SenderAddress: "0x1", FunctionID: "0x1::m::f", WalletID: "w",
			TxnHash: "0x" + string(rune('a'+i)),
			// Stagger UpdatedAt so the (updated_at, id) cursor advances deterministically.
			CreatedAt: now, UpdatedAt: now.Add(time.Duration(i) * time.Millisecond), ExpiresAt: exp,
		})
	}
	st := &pagingStubStore{records: records}

	fetch := &mockTxnFetcher{fn: func(string) (*api.Transaction, error) {
		// Pending return so no state mutation; we're measuring pagination, not confirms.
		return &api.Transaction{Inner: &api.PendingTransaction{}}, nil
	}}
	p := &Poller{
		client:           fetch,
		store:            st,
		notifier:         &mockNotifier{},
		interval:         time.Minute,
		pageSize:         3,
		sweepConcurrency: 1,
		logger:           slog.New(slog.DiscardHandler),
	}
	p.poll(context.Background())

	// poll() calls sweepStatus twice (submitted + processing). The processing
	// sweep sees zero rows and issues exactly one ListByStatusPaged call that
	// returns empty. So total pageCalls = submitted pages + 1 empty processing call.
	st.mu.Lock()
	calls := append([]pageCall(nil), st.calls...)
	st.mu.Unlock()
	if len(calls) < 3 {
		t.Fatalf("want >=3 ListByStatusPaged calls for pagination, got %d: %+v", len(calls), calls)
	}
	// First call: zero cursor.
	if !calls[0].cursorTime.IsZero() || calls[0].cursorID != "" {
		t.Errorf("first call should use zero cursor, got %+v", calls[0])
	}
	// Returned sizes for the submitted sweep should be 3, 3, 1.
	wantSizes := []int{3, 3, 1}
	for i, want := range wantSizes {
		if calls[i].returned != want {
			t.Errorf("call %d returned %d rows, want %d", i, calls[i].returned, want)
		}
	}
	// Cursor should advance: call[1].cursorTime == call[0]'s last row UpdatedAt.
	if !calls[1].cursorTime.Equal(records[2].UpdatedAt) || calls[1].cursorID != records[2].ID {
		t.Errorf("cursor did not advance: call[1] cursor=(%v,%s) want (%v,%s)",
			calls[1].cursorTime, calls[1].cursorID, records[2].UpdatedAt, records[2].ID)
	}
	if !calls[2].cursorTime.Equal(records[5].UpdatedAt) || calls[2].cursorID != records[5].ID {
		t.Errorf("cursor did not advance: call[2] cursor=(%v,%s) want (%v,%s)",
			calls[2].cursorTime, calls[2].cursorID, records[5].UpdatedAt, records[5].ID)
	}
	// Fetcher should have been called once per submitted row.
	fetch.mu.Lock()
	gotCalls := fetch.calls
	fetch.mu.Unlock()
	if gotCalls != 7 {
		t.Errorf("fetcher calls=%d want 7", gotCalls)
	}
}

// TestPollerProcessesPageInParallel proves that within a single page the
// worker pool dispatches multiple confirmRecord goroutines concurrently.
// The fetcher blocks until it has seen `concurrency` simultaneous callers;
// if the poller were serial, only one would ever be in flight at once and
// the test would time out.
func TestPollerProcessesPageInParallel(t *testing.T) {
	const concurrency = 4
	now := time.Now().UTC()
	exp := now.Add(time.Hour)
	var records []*store.TransactionRecord
	for i := range concurrency {
		records = append(records, &store.TransactionRecord{
			ID: "par-" + string(rune('a'+i)), Status: store.StatusSubmitted,
			SenderAddress: "0x1", FunctionID: "0x1::m::f", WalletID: "w",
			TxnHash:   "0xp" + string(rune('a'+i)),
			CreatedAt: now, UpdatedAt: now.Add(time.Duration(i) * time.Millisecond), ExpiresAt: exp,
		})
	}
	st := &pagingStubStore{records: records}

	inFlight := make(chan struct{}, concurrency)
	release := make(chan struct{})
	var peak int64
	var peakMu sync.Mutex
	fetch := &mockTxnFetcher{fn: func(string) (*api.Transaction, error) {
		inFlight <- struct{}{}
		peakMu.Lock()
		if int64(len(inFlight)) > peak {
			peak = int64(len(inFlight))
		}
		peakMu.Unlock()
		<-release
		<-inFlight
		return &api.Transaction{Inner: &api.PendingTransaction{}}, nil
	}}

	p := &Poller{
		client:           fetch,
		store:            st,
		notifier:         &mockNotifier{},
		interval:         time.Minute,
		pageSize:         concurrency * 2,
		sweepConcurrency: concurrency,
		logger:           slog.New(slog.DiscardHandler),
	}

	done := make(chan struct{})
	go func() {
		p.poll(context.Background())
		close(done)
	}()

	// Wait until all `concurrency` fetchers have entered the fn simultaneously.
	deadline := time.After(2 * time.Second)
	for {
		peakMu.Lock()
		p := peak
		peakMu.Unlock()
		if p >= int64(concurrency) {
			break
		}
		select {
		case <-deadline:
			peakMu.Lock()
			got := peak
			peakMu.Unlock()
			t.Fatalf("only %d concurrent fetchers observed (want %d); parallelism is broken", got, concurrency)
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(release)
	<-done
}

// TestPollerProcessingRecoveryRequireHash verifies the processing-status
// recovery path skips records without a txn hash (never safe to confirm
// without re-signing) and processes ones with a pre-persisted hash.
func TestPollerProcessingRecoveryRequireHash(t *testing.T) {
	st := store.NewMemoryStore(time.Hour)
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	exp := now.Add(time.Hour)

	// Two processing rows: one with pre-persisted hash (safe to confirm),
	// one without (must be skipped by requireHash filter).
	withHash := &store.TransactionRecord{
		ID: "proc-with", Status: store.StatusProcessing,
		SenderAddress: "0x1", FunctionID: "0x1::m::f", WalletID: "w",
		TxnHash: "0xproc", CreatedAt: now, UpdatedAt: now, ExpiresAt: exp,
	}
	withoutHash := &store.TransactionRecord{
		ID: "proc-without", Status: store.StatusProcessing,
		SenderAddress: "0x1", FunctionID: "0x1::m::f", WalletID: "w",
		TxnHash: "", CreatedAt: now, UpdatedAt: now, ExpiresAt: exp,
	}
	if err := st.Create(context.Background(), withHash); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(context.Background(), withoutHash); err != nil {
		t.Fatal(err)
	}

	fetch := &mockTxnFetcher{fn: func(hash string) (*api.Transaction, error) {
		if hash != "0xproc" {
			t.Errorf("unexpected hash fetched: %q", hash)
		}
		return userTxn(true, ""), nil
	}}
	n := &mockNotifier{}
	p := &Poller{
		client: fetch, store: st, notifier: n, interval: time.Minute,
		pageSize: 100, sweepConcurrency: 1, logger: slog.New(slog.DiscardHandler),
	}
	p.poll(context.Background())

	// Exactly one fetch — only the hashed processing row is eligible.
	fetch.mu.Lock()
	calls := fetch.calls
	fetch.mu.Unlock()
	if calls != 1 {
		t.Errorf("fetcher calls=%d want 1 (only the hashed processing row)", calls)
	}
	if n.calls() != 1 {
		t.Errorf("notifier calls=%d want 1", n.calls())
	}

	// The hashed row advanced to confirmed; the empty-hash row stayed in processing.
	got, _ := st.Get(context.Background(), "proc-with")
	if got == nil || got.Status != store.StatusConfirmed {
		t.Errorf("proc-with: %#v", got)
	}
	got, _ = st.Get(context.Background(), "proc-without")
	if got == nil || got.Status != store.StatusProcessing {
		t.Errorf("proc-without should remain processing: %#v", got)
	}
}

// TestPollerNoLimiterByDefault verifies poll() with rpcRPS=0 does not throttle
// and makes all its calls immediately.
func TestPollerNoLimiterByDefault(t *testing.T) {
	t.Parallel()
	st := store.NewMemoryStore(time.Hour)
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	exp := now.Add(time.Hour)
	for i := range 5 {
		rec := &store.TransactionRecord{
			ID: "nl-" + string(rune('a'+i)), Status: store.StatusSubmitted,
			SenderAddress: "0x1", FunctionID: "0x1::m::f", WalletID: "w",
			TxnHash: "0xn" + string(rune('a'+i)), CreatedAt: now, UpdatedAt: now, ExpiresAt: exp,
		}
		if err := st.Create(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	fetch := &mockTxnFetcher{fn: func(string) (*api.Transaction, error) {
		return &api.Transaction{Inner: &api.PendingTransaction{}}, nil
	}}
	p := New(fetch, st, &mockNotifier{}, time.Minute, 0, 0, 0, 0, slog.New(slog.DiscardHandler))

	start := time.Now()
	p.poll(context.Background())
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("unexpected throttling with rpcRPS=0: %v", elapsed)
	}
}
