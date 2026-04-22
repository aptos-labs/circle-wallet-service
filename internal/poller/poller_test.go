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

func (m *mockTxnFetcher) TransactionByHash(hash string) (*api.Transaction, error) {
	m.mu.Lock()
	m.calls++
	fn := m.fn
	m.mu.Unlock()
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
	p := New(&mockTxnFetcher{}, st, &mockNotifier{}, time.Millisecond, 0, 0, slog.New(slog.DiscardHandler))
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
	p := New(fetch, st, &mockNotifier{}, time.Minute, 2, 2, slog.New(slog.DiscardHandler))

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
	p := New(fetch, st, &mockNotifier{}, time.Minute, 0, 0, slog.New(slog.DiscardHandler))

	start := time.Now()
	p.poll(context.Background())
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("unexpected throttling with rpcRPS=0: %v", elapsed)
	}
}
