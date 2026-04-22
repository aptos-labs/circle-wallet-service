package submitter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
	"github.com/aptos-labs/aptos-go-sdk/crypto"
	"github.com/aptos-labs/jc-contract-integration/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/internal/circle"
	"github.com/aptos-labs/jc-contract-integration/internal/config"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

func TestIsSequenceError(t *testing.T) {
	t.Parallel()
	if !isSequenceError(&testStringErr{"SEQUENCE_NUMBER out of range"}) {
		t.Fatal("expected true for SEQUENCE_NUMBER")
	}
	if !isSequenceError(&testStringErr{"sequence_number too old"}) {
		t.Fatal("expected true for sequence_number")
	}
	if isSequenceError(&testStringErr{"INSUFFICIENT_BALANCE"}) {
		t.Fatal("expected false for INSUFFICIENT_BALANCE")
	}
	if isSequenceError(nil) {
		t.Fatal("expected false for nil")
	}
}

type testStringErr struct{ s string }

func (e *testStringErr) Error() string { return e.s }

func TestRetrySleep(t *testing.T) {
	t.Parallel()
	t.Run("Canceled", func(t *testing.T) {
		t.Parallel()
		s := &Submitter{
			cfg: &config.Config{
				Submitter: config.SubmitterConfig{
					RetryIntervalSeconds: 30,
					RetryJitterSeconds:   0,
				},
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		s.retrySleep(ctx)
		if d := time.Since(start); d > 50*time.Millisecond {
			t.Fatalf("expected quick return on cancel, took %v", d)
		}
	})
	t.Run("DurationBounded", func(t *testing.T) {
		t.Parallel()
		s := &Submitter{
			cfg: &config.Config{
				Submitter: config.SubmitterConfig{
					RetryIntervalSeconds: 0,
					RetryJitterSeconds:   2,
				},
			},
		}
		const workers = 24
		var wg sync.WaitGroup
		wg.Add(workers)
		for i := 0; i < workers; i++ {
			go func() {
				defer wg.Done()
				start := time.Now()
				s.retrySleep(context.Background())
				if d := time.Since(start); d > 2500*time.Millisecond {
					t.Errorf("sleep too long: %v", d)
				}
			}()
		}
		wg.Wait()
	})
}

func TestDispatcher(t *testing.T) {
	var listCalls atomic.Int32
	mq := &mockQueue{
		listQueuedSenders: func(ctx context.Context) ([]string, error) {
			if listCalls.Add(1) == 1 {
				return []string{"0xsenderaaa", "0xsenderbbb"}, nil
			}
			return nil, nil
		},
		claimNextQueuedForSender: func(ctx context.Context, sender string) (*store.TransactionRecord, error) {
			return nil, nil
		},
	}
	cfg := &config.Config{
		Submitter: config.SubmitterConfig{
			PollIntervalMs:          15,
			RecoveryTickSeconds:     3600,
			MaxRetryDurationSeconds: 300,
			RetryIntervalSeconds:    0,
			RetryJitterSeconds:      0,
			SigningPipelineDepth:    1,
		},
	}
	s := New(cfg, mq, nil, nil, nil, nil, noopNotifier{}, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go s.Run(ctx)
	<-ctx.Done()

	if n := mq.loadClaimCalls(); n < 2 {
		t.Fatalf("expected at least 2 claim attempts (one per worker), got %d", n)
	}
}

func TestPermanentFailureShifts(t *testing.T) {
	t.Parallel()
	seq := uint64(7)
	rec := &store.TransactionRecord{
		ID:             "tid-1",
		SenderAddress:  "0x1",
		Status:         store.StatusProcessing,
		SequenceNumber: &seq,
	}
	var n notifyRecorder
	mq := &mockQueue{}
	s := &Submitter{
		cfg:      &config.Config{},
		queue:    mq,
		notifier: &n,
		logger:   slog.New(slog.DiscardHandler),
	}
	s.markPermanentFailure(context.Background(), rec, "validation", "permanent")

	if n.count != 1 {
		t.Fatalf("notify count %d", n.count)
	}
	if mq.shiftCount != 1 || mq.shiftSender != rec.SenderAddress || mq.shiftSeq != seq {
		t.Fatalf("shift mismatch: count=%d sender=%q seq=%d", mq.shiftCount, mq.shiftSender, mq.shiftSeq)
	}
	if mq.lastUpdate == nil || mq.lastUpdate.Status != store.StatusFailed || mq.lastUpdate.ErrorMessage != "permanent" {
		t.Fatalf("update record: %+v", mq.lastUpdate)
	}
	if mq.lastUpdate.FailureKind != "validation" {
		t.Fatalf("failure kind %q", mq.lastUpdate.FailureKind)
	}
}

func TestMarkSimulationFailureSetsVmStatus(t *testing.T) {
	t.Parallel()
	seq := uint64(3)
	rec := &store.TransactionRecord{
		ID:             "tid-sim",
		SenderAddress:  "0x1",
		Status:         store.StatusProcessing,
		SequenceNumber: &seq,
	}
	var n notifyRecorder
	mq := &mockQueue{}
	s := &Submitter{
		cfg:      &config.Config{},
		queue:    mq,
		notifier: &n,
		logger:   slog.New(slog.DiscardHandler),
	}
	s.markSimulationFailure(context.Background(), rec, "Move abort EINSUFFICIENT_BALANCE", "simulation rejected")

	if mq.lastUpdate == nil {
		t.Fatal("expected Update")
	}
	if mq.lastUpdate.FailureKind != "simulation" {
		t.Fatalf("failure kind %q", mq.lastUpdate.FailureKind)
	}
	if mq.lastUpdate.VmStatus != "Move abort EINSUFFICIENT_BALANCE" {
		t.Fatalf("vm status %q", mq.lastUpdate.VmStatus)
	}
	if mq.shiftCount != 1 {
		t.Fatalf("expected shift to unblock siblings, got %d", mq.shiftCount)
	}
	if n.count != 1 {
		t.Fatalf("expected webhook notify, got %d", n.count)
	}
}

func TestTransientFailureRequeues(t *testing.T) {
	t.Parallel()
	rec := &store.TransactionRecord{
		ID:             "r1",
		SenderAddress:  "0x1",
		Status:         store.StatusProcessing,
		SequenceNumber: nil,
		CreatedAt:      time.Now().UTC().Add(-time.Minute),
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
	}

	mq := &mockQueue{}
	s := &Submitter{
		cfg: &config.Config{
			Submitter: config.SubmitterConfig{MaxRetryDurationSeconds: 600},
		},
		queue:  mq,
		logger: slog.New(slog.DiscardHandler),
	}
	item, transient := s.prepareRecord(context.Background(), rec)
	if item != nil || !transient {
		t.Fatalf("expected transient nil item, got item=%v transient=%v", item != nil, transient)
	}
	if mq.lastUpdate == nil {
		t.Fatal("expected Update")
	}
	if mq.lastUpdate.Status != store.StatusQueued {
		t.Fatalf("status %q", mq.lastUpdate.Status)
	}
	if mq.lastUpdate.AttemptCount != 1 {
		t.Fatalf("attempt %d", mq.lastUpdate.AttemptCount)
	}
	if mq.lastUpdate.SequenceNumber != nil {
		t.Fatal("expected sequence cleared")
	}
}

func TestPrepareRecord_ExpiredMarksFailed(t *testing.T) {
	t.Parallel()
	seq := uint64(1)
	rec := &store.TransactionRecord{
		ID:             "r2",
		SenderAddress:  "0x1",
		Status:         store.StatusProcessing,
		SequenceNumber: &seq,
		CreatedAt:      time.Now().UTC().Add(-time.Hour),
		ExpiresAt:      time.Now().UTC().Add(-time.Minute),
	}
	var n notifyRecorder
	mq := &mockQueue{}
	s := &Submitter{
		cfg:      &config.Config{},
		queue:    mq,
		notifier: &n,
		logger:   slog.New(slog.DiscardHandler),
	}
	item, transient := s.prepareRecord(context.Background(), rec)
	if item != nil || transient {
		t.Fatalf("expected permanent failure, item=%v transient=%v", item != nil, transient)
	}
	if mq.lastUpdate == nil || mq.lastUpdate.Status != store.StatusFailed {
		t.Fatalf("status: %+v", mq.lastUpdate)
	}
	if n.count != 1 {
		t.Fatalf("expected notifier called once, got %d", n.count)
	}
}

func TestPrepareRecord_MaxRetryDurationMarksFailed(t *testing.T) {
	t.Parallel()
	seq := uint64(1)
	rec := &store.TransactionRecord{
		ID:             "r3",
		SenderAddress:  "0x1",
		Status:         store.StatusProcessing,
		SequenceNumber: &seq,
		CreatedAt:      time.Now().UTC().Add(-400 * time.Second),
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
	}
	mq := &mockQueue{}
	s := &Submitter{
		cfg: &config.Config{
			Submitter: config.SubmitterConfig{MaxRetryDurationSeconds: 300},
		},
		queue:    mq,
		notifier: &notifyRecorder{},
		logger:   slog.New(slog.DiscardHandler),
	}
	item, transient := s.prepareRecord(context.Background(), rec)
	if item != nil || transient {
		t.Fatalf("expected permanent failure, item=%v transient=%v", item != nil, transient)
	}
	if mq.lastUpdate == nil || mq.lastUpdate.Status != store.StatusFailed {
		t.Fatalf("status: %+v", mq.lastUpdate)
	}
	if mq.lastUpdate.ErrorMessage == "" {
		t.Fatal("expected error message")
	}
}

type noopNotifier struct{}

func (noopNotifier) Notify(context.Context, *store.TransactionRecord) {}

type notifyRecorder struct {
	count int
}

func (n *notifyRecorder) Notify(_ context.Context, _ *store.TransactionRecord) {
	n.count++
}

type mockQueue struct {
	mu sync.Mutex

	listQueuedSenders        func(ctx context.Context) ([]string, error)
	claimNextQueuedForSender func(ctx context.Context, sender string) (*store.TransactionRecord, error)
	recoverStaleProcessing   func(ctx context.Context, olderThan time.Duration) (int64, error)

	claimCalls   atomic.Int32
	recoverCalls atomic.Int32

	updateErr error

	updateIfStatusFn func(ctx context.Context, rec *store.TransactionRecord, expected store.TxnStatus) (bool, error)

	lastUpdate  *store.TransactionRecord
	shiftCount  int
	shiftSender string
	shiftSeq    uint64
	shiftErr    error

	releaseCount  int
	releaseSender string

	reconcileCount int
	reconcileSeq   uint64

	forceResetCount int
	forceResetSeq   uint64

	listByStatus func(ctx context.Context, status store.TxnStatus) ([]*store.TransactionRecord, error)
}

func (m *mockQueue) loadClaimCalls() int {
	return int(m.claimCalls.Load())
}

func (m *mockQueue) ListQueuedSenders(ctx context.Context) ([]string, error) {
	m.mu.Lock()
	fn := m.listQueuedSenders
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return nil, nil
}

func (m *mockQueue) ClaimNextQueuedForSender(ctx context.Context, sender string) (*store.TransactionRecord, error) {
	m.claimCalls.Add(1)
	m.mu.Lock()
	fn := m.claimNextQueuedForSender
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, sender)
	}
	return nil, nil
}

func (m *mockQueue) Update(ctx context.Context, rec *store.TransactionRecord) error {
	m.mu.Lock()
	err := m.updateErr
	if err == nil {
		cp := *rec
		m.lastUpdate = &cp
	}
	m.mu.Unlock()
	return err
}

func (m *mockQueue) ShiftSenderSequences(ctx context.Context, senderAddress string, failedSeqNum uint64) error {
	m.mu.Lock()
	m.shiftCount++
	m.shiftSender = senderAddress
	m.shiftSeq = failedSeqNum
	err := m.shiftErr
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return nil
}

func (m *mockQueue) Create(ctx context.Context, rec *store.TransactionRecord) error { return nil }
func (m *mockQueue) UpdateIfStatus(ctx context.Context, rec *store.TransactionRecord, expectedStatus store.TxnStatus) (bool, error) {
	m.mu.Lock()
	fn := m.updateIfStatusFn
	err := m.updateErr
	if err == nil && fn == nil {
		cp := *rec
		m.lastUpdate = &cp
	}
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, rec, expectedStatus)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (m *mockQueue) Get(ctx context.Context, id string) (*store.TransactionRecord, error) {
	return nil, nil
}

func (m *mockQueue) GetByIdempotencyKey(ctx context.Context, key string) (*store.TransactionRecord, error) {
	return nil, nil
}

func (m *mockQueue) ListByStatus(ctx context.Context, status store.TxnStatus) ([]*store.TransactionRecord, error) {
	m.mu.Lock()
	fn := m.listByStatus
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, status)
	}
	return nil, nil
}
func (m *mockQueue) Close() error { return nil }

func (m *mockQueue) ReconcileSequence(ctx context.Context, senderAddress string, chainSeq uint64) error {
	m.mu.Lock()
	m.reconcileCount++
	m.reconcileSeq = chainSeq
	m.mu.Unlock()
	return nil
}

func (m *mockQueue) ForceResetSequenceToChain(ctx context.Context, senderAddress string, chainSeq uint64) error {
	m.mu.Lock()
	m.forceResetCount++
	m.forceResetSeq = chainSeq
	m.mu.Unlock()
	return nil
}

func (m *mockQueue) ReleaseSequence(ctx context.Context, senderAddress string) error {
	m.mu.Lock()
	m.releaseCount++
	m.releaseSender = senderAddress
	m.mu.Unlock()
	return nil
}

func (m *mockQueue) RecoverStaleProcessing(ctx context.Context, olderThan time.Duration) (int64, error) {
	m.recoverCalls.Add(1)
	m.mu.Lock()
	fn := m.recoverStaleProcessing
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, olderThan)
	}
	return 0, nil
}

func testCircleWallet(t *testing.T) (walletID, senderAddress, publicKeyHex string) {
	t.Helper()
	priv, err := crypto.GenerateEd25519PrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := priv.PubKey().(*crypto.Ed25519PublicKey)
	if !ok {
		t.Fatal("expected Ed25519 public key")
	}
	var addr aptossdk.AccountAddress
	addr.FromAuthKey(pub.AuthKey())
	return "w-submitter-test", addr.StringLong(), pub.ToHex()
}

func mustJSONQueuedPayload(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(store.QueuedPayload{TypeArguments: []string{}, Arguments: []any{}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRecoverLoop(t *testing.T) {
	t.Parallel()
	mq := &mockQueue{
		recoverStaleProcessing: func(ctx context.Context, olderThan time.Duration) (int64, error) {
			return 1, nil
		},
	}
	cfg := &config.Config{
		Submitter: config.SubmitterConfig{
			RecoveryTickSeconds:    1,
			StaleProcessingSeconds: 30,
		},
	}
	s := &Submitter{cfg: cfg, queue: mq, logger: slog.New(slog.DiscardHandler)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.recoverLoop(ctx)
		close(done)
	}()
	time.Sleep(2500 * time.Millisecond)
	if mq.recoverCalls.Load() < 2 {
		t.Fatalf("expected at least 2 recover calls, got %d", mq.recoverCalls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("recoverLoop did not exit")
	}
}

func TestRunSenderWorker(t *testing.T) {
	t.Parallel()
	walletID, senderAddr, pubHex := testCircleWallet(t)
	seq := uint64(0)
	var nCalls atomic.Int32
	rec := &store.TransactionRecord{
		ID:             "job-1",
		SenderAddress:  senderAddr,
		WalletID:       walletID,
		Status:         store.StatusProcessing,
		SequenceNumber: &seq,
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
		CreatedAt:      time.Now().UTC(),
		PayloadJSON:    mustJSONQueuedPayload(t),
		FunctionID:     "not-valid-function-id",
	}
	pkCache := circle.NewPublicKeyCache(nil)
	pkCache.Set(walletID, pubHex)
	mq := &mockQueue{
		claimNextQueuedForSender: func(ctx context.Context, sender string) (*store.TransactionRecord, error) {
			n := nCalls.Add(1)
			if n == 1 {
				if sender != senderAddr {
					t.Errorf("sender %q want %q", sender, senderAddr)
				}
				return rec, nil
			}
			return nil, nil
		},
	}
	s := &Submitter{
		cfg: &config.Config{
			Submitter: config.SubmitterConfig{
				SigningPipelineDepth:    1,
				RetryIntervalSeconds:    0,
				RetryJitterSeconds:      0,
				MaxRetryDurationSeconds: 600,
			},
		},
		queue:    mq,
		client:   nil,
		abi:      aptos.NewABICache(nil),
		signer:   nil,
		pkCache:  pkCache,
		notifier: noopNotifier{},
		logger:   slog.New(slog.DiscardHandler),
	}
	s.runSenderWorker(context.Background(), senderAddr)
	if nCalls.Load() < 2 {
		t.Fatalf("expected at least 2 claims, got %d", nCalls.Load())
	}
	if mq.lastUpdate == nil {
		t.Fatal("expected queue Update")
	}
	if mq.lastUpdate.Status != store.StatusFailed {
		t.Fatalf("status %q", mq.lastUpdate.Status)
	}
}

func TestMarkPermanentFailure_NoSequence(t *testing.T) {
	t.Parallel()
	rec := &store.TransactionRecord{
		ID:             "tid-2",
		SenderAddress:  "0x1",
		Status:         store.StatusProcessing,
		SequenceNumber: nil,
	}
	mq := &mockQueue{}
	s := &Submitter{
		cfg:      &config.Config{},
		queue:    mq,
		notifier: noopNotifier{},
		logger:   slog.New(slog.DiscardHandler),
	}
	s.markPermanentFailure(context.Background(), rec, "submit", "boom")
	if mq.shiftCount != 0 {
		t.Fatalf("ShiftSenderSequences calls=%d", mq.shiftCount)
	}
	if mq.lastUpdate == nil || mq.lastUpdate.Status != store.StatusFailed {
		t.Fatalf("update: %+v", mq.lastUpdate)
	}
}

func TestRequeueTransient(t *testing.T) {
	t.Parallel()
	rec := &store.TransactionRecord{
		ID:            "r-q",
		SenderAddress: "0x1",
		Status:        store.StatusProcessing,
		AttemptCount:  2,
	}
	mq := &mockQueue{}
	s := &Submitter{
		queue:  mq,
		logger: slog.New(slog.DiscardHandler),
	}
	s.requeueTransient(context.Background(), rec, &testStringErr{"transient err"})
	if mq.lastUpdate == nil {
		t.Fatal("expected Update")
	}
	if mq.lastUpdate.Status != store.StatusQueued {
		t.Fatalf("status %q", mq.lastUpdate.Status)
	}
	if mq.lastUpdate.AttemptCount != 3 {
		t.Fatalf("attempt %d", mq.lastUpdate.AttemptCount)
	}
	if mq.lastUpdate.LastError != "transient err" {
		t.Fatalf("last error %q", mq.lastUpdate.LastError)
	}
	if mq.lastUpdate.SequenceNumber != nil {
		t.Fatal("expected sequence cleared")
	}
}

func TestDrainPipeline(t *testing.T) {
	t.Parallel()
	mq := &mockQueue{}
	s := &Submitter{
		queue:  mq,
		logger: slog.New(slog.DiscardHandler),
	}
	rec := &store.TransactionRecord{ID: "drain-1", SenderAddress: "0x1"}
	ch := make(chan signedItem, 1)
	ch <- signedItem{rec: rec}
	close(ch)
	s.drainPipeline(context.Background(), ch, "@@@not-a-valid-aptos-address@@@")
	if mq.lastUpdate == nil || mq.lastUpdate.ID != "drain-1" {
		t.Fatalf("requeue: %+v", mq.lastUpdate)
	}
	if mq.lastUpdate.Status != store.StatusQueued {
		t.Fatalf("status %q", mq.lastUpdate.Status)
	}
}

func TestDispatcherSpawnsWorkersForMultipleSenders(t *testing.T) {
	mq := &mockQueue{
		listQueuedSenders: func(ctx context.Context) ([]string, error) {
			return []string{"0xs1", "0xs2", "0xs3"}, nil
		},
		claimNextQueuedForSender: func(ctx context.Context, sender string) (*store.TransactionRecord, error) {
			return nil, nil
		},
	}
	cfg := &config.Config{
		Submitter: config.SubmitterConfig{
			PollIntervalMs:          10,
			RecoveryTickSeconds:     3600,
			MaxRetryDurationSeconds: 300,
			RetryIntervalSeconds:    0,
			RetryJitterSeconds:      0,
			SigningPipelineDepth:    1,
		},
	}
	s := New(cfg, mq, nil, nil, nil, nil, noopNotifier{}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go s.Run(ctx)
	for start := time.Now(); time.Since(start) < 250*time.Millisecond; time.Sleep(5 * time.Millisecond) {
		if mq.loadClaimCalls() >= 3 {
			return
		}
	}
	t.Fatalf("expected 3 claim attempts, got %d", mq.loadClaimCalls())
}

func TestDispatcherDoesNotDuplicateWorkers(t *testing.T) {
	slot := make(chan struct{}, 1)
	mq := &mockQueue{
		listQueuedSenders: func(ctx context.Context) ([]string, error) {
			return []string{"0xsolo"}, nil
		},
		claimNextQueuedForSender: func(ctx context.Context, sender string) (*store.TransactionRecord, error) {
			select {
			case slot <- struct{}{}:
			default:
				t.Error("concurrent claim for same sender")
				return nil, nil
			}
			defer func() { <-slot }()
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	cfg := &config.Config{
		Submitter: config.SubmitterConfig{
			PollIntervalMs:          10,
			RecoveryTickSeconds:     3600,
			MaxRetryDurationSeconds: 300,
			RetryIntervalSeconds:    0,
			RetryJitterSeconds:      0,
			SigningPipelineDepth:    1,
		},
	}
	s := New(cfg, mq, nil, nil, nil, nil, noopNotifier{}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	go s.Run(ctx)
	<-ctx.Done()
}

func TestWorkerExitsAndCanRespawn(t *testing.T) {
	mq := &mockQueue{
		listQueuedSenders: func(ctx context.Context) ([]string, error) {
			return []string{"0xrespawn"}, nil
		},
		claimNextQueuedForSender: func(ctx context.Context, sender string) (*store.TransactionRecord, error) {
			return nil, nil
		},
	}
	cfg := &config.Config{
		Submitter: config.SubmitterConfig{
			PollIntervalMs:          15,
			RecoveryTickSeconds:     3600,
			MaxRetryDurationSeconds: 300,
			RetryIntervalSeconds:    0,
			RetryJitterSeconds:      0,
			SigningPipelineDepth:    1,
		},
	}
	s := New(cfg, mq, nil, nil, nil, nil, noopNotifier{}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go s.Run(ctx)
	for start := time.Now(); time.Since(start) < 180*time.Millisecond; time.Sleep(5 * time.Millisecond) {
		if mq.loadClaimCalls() >= 2 {
			return
		}
	}
	t.Fatalf("expected respawned worker (>=2 claims), got %d", mq.loadClaimCalls())
}

func TestPrepareRecord_BadPayloadJSON(t *testing.T) {
	t.Parallel()
	walletID, senderAddr, pubHex := testCircleWallet(t)
	seq := uint64(1)
	pkCache := circle.NewPublicKeyCache(nil)
	pkCache.Set(walletID, pubHex)
	rec := &store.TransactionRecord{
		ID:             "bad-json",
		WalletID:       walletID,
		SenderAddress:  senderAddr,
		SequenceNumber: &seq,
		CreatedAt:      time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
		PayloadJSON:    "{",
		FunctionID:     "ignored",
	}
	mq := &mockQueue{}
	s := &Submitter{
		cfg:      &config.Config{},
		queue:    mq,
		abi:      nil,
		pkCache:  pkCache,
		notifier: noopNotifier{},
		logger:   slog.New(slog.DiscardHandler),
	}
	item, transient := s.prepareRecord(context.Background(), rec)
	if item != nil || transient {
		t.Fatalf("want permanent failure, item=%v transient=%v", item != nil, transient)
	}
	if mq.lastUpdate == nil || mq.lastUpdate.Status != store.StatusFailed {
		t.Fatalf("update %+v", mq.lastUpdate)
	}
}

func TestPrepareRecord_WalletAddressMismatch(t *testing.T) {
	t.Parallel()
	walletID, senderAddr, pubHex := testCircleWallet(t)
	seq := uint64(1)
	pkCache := circle.NewPublicKeyCache(nil)
	pkCache.Set(walletID, pubHex)
	rec := &store.TransactionRecord{
		ID:             "addr-mismatch",
		WalletID:       walletID,
		SenderAddress:  senderAddr + "00",
		SequenceNumber: &seq,
		CreatedAt:      time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
		PayloadJSON:    mustJSONQueuedPayload(t),
		FunctionID:     "ignored",
	}
	mq := &mockQueue{}
	s := &Submitter{
		cfg:      &config.Config{},
		queue:    mq,
		abi:      nil,
		pkCache:  pkCache,
		notifier: noopNotifier{},
		logger:   slog.New(slog.DiscardHandler),
	}
	item, transient := s.prepareRecord(context.Background(), rec)
	if item != nil || transient {
		t.Fatalf("want permanent failure, item=%v transient=%v", item != nil, transient)
	}
	if mq.lastUpdate == nil || mq.lastUpdate.Status != store.StatusFailed {
		t.Fatalf("update %+v", mq.lastUpdate)
	}
}

func TestRun_ListQueuedSendersError(t *testing.T) {
	var listCalls atomic.Int32
	mq := &mockQueue{
		listQueuedSenders: func(ctx context.Context) ([]string, error) {
			if listCalls.Add(1) == 1 {
				return nil, errors.New("list failed")
			}
			return nil, nil
		},
	}
	cfg := &config.Config{
		Submitter: config.SubmitterConfig{
			PollIntervalMs:          20,
			RecoveryTickSeconds:     3600,
			MaxRetryDurationSeconds: 300,
			RetryIntervalSeconds:    0,
			RetryJitterSeconds:      0,
			SigningPipelineDepth:    1,
		},
	}
	s := New(cfg, mq, nil, nil, nil, nil, noopNotifier{}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	go s.Run(ctx)
	<-ctx.Done()
	if listCalls.Load() < 2 {
		t.Fatalf("expected list retry after error, calls=%d", listCalls.Load())
	}
}

func TestRunSenderWorker_ClaimErrorRetry(t *testing.T) {
	t.Parallel()
	var claimN atomic.Int32
	mq := &mockQueue{
		claimNextQueuedForSender: func(ctx context.Context, sender string) (*store.TransactionRecord, error) {
			if claimN.Add(1) == 1 {
				return nil, errors.New("claim failed")
			}
			return nil, nil
		},
	}
	cfg := &config.Config{
		Submitter: config.SubmitterConfig{
			SigningPipelineDepth:    1,
			RetryIntervalSeconds:    0,
			RetryJitterSeconds:      0,
			MaxRetryDurationSeconds: 600,
		},
	}
	s := &Submitter{
		cfg:    cfg,
		queue:  mq,
		logger: slog.New(slog.DiscardHandler),
	}
	s.runSenderWorker(context.Background(), "0xclaim")
	if claimN.Load() < 2 {
		t.Fatalf("claims=%d", claimN.Load())
	}
}

func TestRequeueRecord(t *testing.T) {
	t.Parallel()
	rec := &store.TransactionRecord{
		ID:            "rq",
		SenderAddress: "0x1",
		Status:        store.StatusProcessing,
		AttemptCount:  3,
	}
	mq := &mockQueue{}
	s := &Submitter{
		queue:  mq,
		logger: slog.New(slog.DiscardHandler),
	}
	s.requeueRecord(context.Background(), rec)
	if mq.lastUpdate == nil || mq.lastUpdate.Status != store.StatusQueued {
		t.Fatalf("update %+v", mq.lastUpdate)
	}
	if mq.lastUpdate.AttemptCount != 3 {
		t.Fatalf("attempt count should be unchanged, got %d", mq.lastUpdate.AttemptCount)
	}
}

func TestMarkPermanentFailure_UpdateError(t *testing.T) {
	t.Parallel()
	seq := uint64(3)
	rec := &store.TransactionRecord{
		ID:             "up-fail",
		SenderAddress:  "0x1",
		Status:         store.StatusProcessing,
		SequenceNumber: &seq,
	}
	mq := &mockQueue{updateErr: errors.New("persist failed")}
	var n notifyRecorder
	s := &Submitter{
		cfg:      &config.Config{},
		queue:    mq,
		notifier: &n,
		logger:   slog.New(slog.DiscardHandler),
	}
	s.markPermanentFailure(context.Background(), rec, "validation", "bad")
	if n.count != 1 {
		t.Fatalf("notify count=%d", n.count)
	}
	if mq.shiftCount != 1 {
		t.Fatalf("expected shift despite update error, got %d", mq.shiftCount)
	}
}

func TestRequeueTransient_UpdateError(t *testing.T) {
	t.Parallel()
	rec := &store.TransactionRecord{ID: "rt-up", SenderAddress: "0x1"}
	mq := &mockQueue{updateErr: errors.New("write failed")}
	s := &Submitter{
		queue:  mq,
		logger: slog.New(slog.DiscardHandler),
	}
	s.requeueTransient(context.Background(), rec, errors.New("transient"))
}

type fakeSubmitter struct {
	hash string
	err  error
}

func (f *fakeSubmitter) SubmitTransaction(_ *aptossdk.SignedTransaction) (*api.SubmitTransactionResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &api.SubmitTransactionResponse{Hash: f.hash}, nil
}

func TestSubmitSigned_Success(t *testing.T) {
	t.Parallel()
	rec := &store.TransactionRecord{
		ID:            "sub-ok",
		SenderAddress: "0x1",
		CreatedAt:     time.Now().UTC(),
	}
	mq := &mockQueue{}
	s := &Submitter{
		queue:    mq,
		txSubmit: &fakeSubmitter{hash: "0xabc"},
		logger:   slog.New(slog.DiscardHandler),
	}
	ok := s.submitSigned(context.Background(), &signedItem{rec: rec, signedTxn: &aptossdk.SignedTransaction{}, seqNum: 1})
	if !ok {
		t.Fatal("expected success")
	}
	if mq.lastUpdate == nil || mq.lastUpdate.Status != store.StatusSubmitted || mq.lastUpdate.TxnHash != "0xabc" {
		t.Fatalf("update %+v", mq.lastUpdate)
	}
}

func TestSubmitSigned_RowNoLongerProcessingBailsWithoutBroadcast(t *testing.T) {
	t.Parallel()
	rec := &store.TransactionRecord{
		ID:            "sub-race",
		SenderAddress: "0x1",
		Status:        store.StatusProcessing,
		CreatedAt:     time.Now().UTC(),
	}
	// Simulate RecoverStaleProcessing winning the race while we were signing:
	// the row is no longer in processing, so UpdateIfStatus is a no-op.
	mq := &mockQueue{
		updateIfStatusFn: func(context.Context, *store.TransactionRecord, store.TxnStatus) (bool, error) {
			return false, nil
		},
	}
	submit := &fakeSubmitter{hash: "MUST_NOT_BE_BROADCAST"}
	s := &Submitter{
		queue:    mq,
		txSubmit: submit,
		logger:   slog.New(slog.DiscardHandler),
	}
	ok := s.submitSigned(context.Background(), &signedItem{
		rec:       rec,
		signedTxn: &aptossdk.SignedTransaction{},
		seqNum:    1,
		hash:      "0xabc",
	})
	if ok {
		t.Fatal("expected submitSigned to return false when row is no longer processing")
	}
	if mq.lastUpdate != nil {
		t.Fatalf("unexpected Update call: %+v — must not mutate a row owned by the recovery path", mq.lastUpdate)
	}
	if mq.releaseCount != 0 {
		t.Fatalf("ReleaseSequence called %d times, want 0 — recovery path already decremented the counter", mq.releaseCount)
	}
	if rec.TxnHash != "" {
		t.Fatalf("expected in-memory TxnHash cleared, got %q", rec.TxnHash)
	}
}

func TestSubmitSigned_UpdateFails(t *testing.T) {
	t.Parallel()
	rec := &store.TransactionRecord{
		ID:            "sub-up",
		SenderAddress: "0x1",
		CreatedAt:     time.Now().UTC(),
	}
	mq := &mockQueue{updateErr: errors.New("db")}
	s := &Submitter{
		queue:    mq,
		txSubmit: &fakeSubmitter{hash: "0xabc"},
		logger:   slog.New(slog.DiscardHandler),
	}
	if s.submitSigned(context.Background(), &signedItem{rec: rec, signedTxn: &aptossdk.SignedTransaction{}, seqNum: 1}) {
		t.Fatal("expected failure")
	}
}

func TestSubmitSigned_NonSequenceErrorRequeues(t *testing.T) {
	t.Parallel()
	rec := &store.TransactionRecord{
		ID:            "sub-rq",
		SenderAddress: "0x1",
		CreatedAt:     time.Now().UTC(),
	}
	mq := &mockQueue{}
	s := &Submitter{
		cfg: &config.Config{
			Submitter: config.SubmitterConfig{MaxRetryDurationSeconds: 600},
		},
		queue:    mq,
		txSubmit: &fakeSubmitter{err: errors.New("rpc down")},
		logger:   slog.New(slog.DiscardHandler),
	}
	if s.submitSigned(context.Background(), &signedItem{rec: rec, signedTxn: &aptossdk.SignedTransaction{}, seqNum: 1}) {
		t.Fatal("expected failure")
	}
	if mq.lastUpdate == nil || mq.lastUpdate.Status != store.StatusQueued {
		t.Fatalf("update %+v", mq.lastUpdate)
	}
	if mq.lastUpdate.LastError != "rpc down" {
		t.Fatalf("last error %q", mq.lastUpdate.LastError)
	}
}

func TestSubmitSigned_PermanentAfterMaxDuration(t *testing.T) {
	t.Parallel()
	rec := &store.TransactionRecord{
		ID:            "sub-dead",
		SenderAddress: "0x1",
		CreatedAt:     time.Now().UTC().Add(-400 * time.Second),
	}
	mq := &mockQueue{}
	s := &Submitter{
		cfg: &config.Config{
			Submitter: config.SubmitterConfig{MaxRetryDurationSeconds: 300},
		},
		queue:    mq,
		txSubmit: &fakeSubmitter{err: errors.New("rpc down")},
		notifier: noopNotifier{},
		logger:   slog.New(slog.DiscardHandler),
	}
	if s.submitSigned(context.Background(), &signedItem{rec: rec, signedTxn: &aptossdk.SignedTransaction{}, seqNum: 1}) {
		t.Fatal("expected failure")
	}
	if mq.lastUpdate == nil || mq.lastUpdate.Status != store.StatusFailed {
		t.Fatalf("update %+v", mq.lastUpdate)
	}
}

func TestSubmitSigned_SequenceErrorUnparseableSender(t *testing.T) {
	t.Parallel()
	rec := &store.TransactionRecord{
		ID:            "sub-seq",
		SenderAddress: "not-parseable-as-aptos",
		CreatedAt:     time.Now().UTC(),
	}
	mq := &mockQueue{}
	s := &Submitter{
		cfg: &config.Config{
			Submitter: config.SubmitterConfig{MaxRetryDurationSeconds: 600},
		},
		queue:    mq,
		txSubmit: &fakeSubmitter{err: &testStringErr{"SEQUENCE_NUMBER out of range"}},
		logger:   slog.New(slog.DiscardHandler),
	}
	if s.submitSigned(context.Background(), &signedItem{rec: rec, signedTxn: &aptossdk.SignedTransaction{}, seqNum: 1}) {
		t.Fatal("expected failure")
	}
	if mq.lastUpdate == nil || mq.lastUpdate.Status != store.StatusQueued {
		t.Fatalf("update %+v", mq.lastUpdate)
	}
}

func TestRecoverLoop_RecoverError(t *testing.T) {
	t.Parallel()
	mq := &mockQueue{
		recoverStaleProcessing: func(ctx context.Context, olderThan time.Duration) (int64, error) {
			return 0, errors.New("recover failed")
		},
	}
	cfg := &config.Config{
		Submitter: config.SubmitterConfig{
			RecoveryTickSeconds:    1,
			StaleProcessingSeconds: 30,
		},
	}
	s := &Submitter{cfg: cfg, queue: mq, logger: slog.New(slog.DiscardHandler)}
	ctx, cancel := context.WithCancel(context.Background())
	go s.recoverLoop(ctx)
	time.Sleep(1200 * time.Millisecond)
	cancel()
	if mq.recoverCalls.Load() < 1 {
		t.Fatalf("expected recover attempts, got %d", mq.recoverCalls.Load())
	}
}

func TestMarkPermanentFailure_ShiftError(t *testing.T) {
	t.Parallel()
	seq := uint64(9)
	rec := &store.TransactionRecord{
		ID:             "shift-err",
		SenderAddress:  "0x1",
		Status:         store.StatusProcessing,
		SequenceNumber: &seq,
	}
	mq := &mockQueue{shiftErr: errors.New("shift failed")}
	s := &Submitter{
		cfg:      &config.Config{},
		queue:    mq,
		notifier: noopNotifier{},
		logger:   slog.New(slog.DiscardHandler),
	}
	s.markPermanentFailure(context.Background(), rec, "submit", "failed")
	if mq.shiftCount != 1 {
		t.Fatalf("shift attempts=%d", mq.shiftCount)
	}
}

// TestRequeueWithoutRelease verifies the reconcile-path requeue helper does
// not touch the sequence counter. Regression test for the drift=1 infinite
// loop: if ReleaseSequence ran here, the counter would decrement by 1 after
// each reconcile, pushing the next allocation back into "too old" territory.
func TestRequeueWithoutRelease(t *testing.T) {
	t.Parallel()
	seq := uint64(44)
	rec := &store.TransactionRecord{
		ID:             "r-no-release",
		SenderAddress:  "0xabc",
		Status:         store.StatusProcessing,
		SequenceNumber: &seq,
	}
	mq := &mockQueue{}
	s := &Submitter{queue: mq, logger: slog.New(slog.DiscardHandler)}

	s.requeueWithoutRelease(context.Background(), rec)

	if mq.releaseCount != 0 {
		t.Fatalf("ReleaseSequence called %d times, expected 0 — counter must not move on reconcile-path requeue", mq.releaseCount)
	}
	if mq.lastUpdate == nil {
		t.Fatal("expected an Update call")
	}
	if mq.lastUpdate.Status != store.StatusQueued {
		t.Fatalf("status = %s, want queued", mq.lastUpdate.Status)
	}
	if mq.lastUpdate.SequenceNumber != nil {
		t.Fatalf("sequence_number should be nil, got %d", *mq.lastUpdate.SequenceNumber)
	}
}

// TestApplyReconcile_DoesNotReleaseCounter is the regression test for the
// drift=1 infinite loop. Scenario: chain is at seq=45, local claimed seq=4,
// submit failed with SEQUENCE_NUMBER_TOO_OLD. After applyReconcile runs:
//   - ReconcileSequence must be called with chainSeq=45
//   - ReleaseSequence must NOT be called (would push counter back to 44 and
//     make the next claim hit TOO_OLD again by drift=1)
//   - ShiftSenderSequences must NOT be called (same reason)
//   - The record must be queued with sequence_number=nil
//   - Processing siblings must be requeued WITHOUT their own release calls
func TestApplyReconcile_DoesNotReleaseCounter(t *testing.T) {
	t.Parallel()
	seq := uint64(4)
	rec := &store.TransactionRecord{
		ID:             "rec-main",
		SenderAddress:  "0xabc",
		Status:         store.StatusProcessing,
		SequenceNumber: &seq,
	}
	siblingSeq := uint64(5)
	sibling := &store.TransactionRecord{
		ID:             "rec-sibling",
		SenderAddress:  "0xabc",
		Status:         store.StatusProcessing,
		SequenceNumber: &siblingSeq,
	}
	otherSenderSeq := uint64(9)
	otherSender := &store.TransactionRecord{
		ID:             "rec-other",
		SenderAddress:  "0xdef",
		Status:         store.StatusProcessing,
		SequenceNumber: &otherSenderSeq,
	}
	mq := &mockQueue{
		listByStatus: func(_ context.Context, _ store.TxnStatus) ([]*store.TransactionRecord, error) {
			return []*store.TransactionRecord{rec, sibling, otherSender}, nil
		},
	}
	s := &Submitter{queue: mq, logger: slog.New(slog.DiscardHandler)}

	s.applyReconcile(context.Background(), rec, 4, 45)

	if mq.reconcileCount != 1 {
		t.Fatalf("ReconcileSequence called %d times, want 1", mq.reconcileCount)
	}
	if mq.reconcileSeq != 45 {
		t.Fatalf("ReconcileSequence chainSeq=%d, want 45", mq.reconcileSeq)
	}
	if mq.releaseCount != 0 {
		t.Fatalf("ReleaseSequence called %d times, want 0 — would push counter back into TOO_OLD zone", mq.releaseCount)
	}
	if mq.shiftCount != 0 {
		t.Fatalf("ShiftSenderSequences called %d times, want 0 — counter must not move after reconcile", mq.shiftCount)
	}
	// lastUpdate should be the main record (it's updated last, after the sibling requeue).
	if mq.lastUpdate == nil {
		t.Fatal("no Update recorded")
	}
	if mq.lastUpdate.ID != rec.ID {
		t.Fatalf("last Update was for %s, want %s", mq.lastUpdate.ID, rec.ID)
	}
	if mq.lastUpdate.Status != store.StatusQueued {
		t.Fatalf("main record status=%s, want queued", mq.lastUpdate.Status)
	}
	if mq.lastUpdate.SequenceNumber != nil {
		t.Fatalf("main record sequence_number=%d, want nil", *mq.lastUpdate.SequenceNumber)
	}
	if mq.lastUpdate.AttemptCount != 1 {
		t.Fatalf("attempt_count=%d, want 1", mq.lastUpdate.AttemptCount)
	}
	// Confirm the sibling was requeued (its row was mutated in place).
	if sibling.Status != store.StatusQueued {
		t.Fatalf("sibling status=%s, want queued", sibling.Status)
	}
	if sibling.SequenceNumber != nil {
		t.Fatalf("sibling sequence_number=%d, want nil", *sibling.SequenceNumber)
	}
	// The different-sender record must be untouched.
	if otherSender.Status != store.StatusProcessing {
		t.Fatalf("otherSender status=%s, want still processing", otherSender.Status)
	}
	if otherSender.SequenceNumber == nil || *otherSender.SequenceNumber != 9 {
		t.Fatalf("otherSender sequence_number mutated, got %v", otherSender.SequenceNumber)
	}
	// chainSeq >= localSeq path: ForceResetSequenceToChain must NOT be used
	// here — that would snap the counter DOWN past pending siblings.
	if mq.forceResetCount != 0 {
		t.Fatalf("ForceResetSequenceToChain called %d times, want 0 on chain-ahead path", mq.forceResetCount)
	}
}

// TestApplyReconcile_ChainBehind_ResetsCounter covers the infinite-loop bug
// where our counter has drifted AHEAD of chain (e.g. a run of simulate
// SEQUENCE_NUMBER_TOO_NEW rejections each burning a slot). In that case the
// GREATEST-only ReconcileSequence is a no-op and the next claim allocates
// an even-further-ahead seq, looping forever. applyReconcile must instead
// call ForceResetSequenceToChain to snap the counter back to chain truth.
func TestApplyReconcile_ChainBehind_ResetsCounter(t *testing.T) {
	t.Parallel()
	seq := uint64(264)
	rec := &store.TransactionRecord{
		ID:             "rec-drifted",
		SenderAddress:  "0xabc",
		Status:         store.StatusProcessing,
		SequenceNumber: &seq,
	}
	mq := &mockQueue{
		listByStatus: func(_ context.Context, _ store.TxnStatus) ([]*store.TransactionRecord, error) {
			return []*store.TransactionRecord{rec}, nil
		},
	}
	s := &Submitter{queue: mq, logger: slog.New(slog.DiscardHandler)}

	// local=264, chain=85 → drift=-179. The fix must go through
	// ForceResetSequenceToChain, not ReconcileSequence.
	s.applyReconcile(context.Background(), rec, 264, 85)

	if mq.forceResetCount != 1 {
		t.Fatalf("ForceResetSequenceToChain called %d times, want 1", mq.forceResetCount)
	}
	if mq.forceResetSeq != 85 {
		t.Fatalf("ForceResetSequenceToChain chainSeq=%d, want 85", mq.forceResetSeq)
	}
	if mq.reconcileCount != 0 {
		t.Fatalf("ReconcileSequence called %d times, want 0 — chain is behind, GREATEST is a no-op and would leave the counter stuck", mq.reconcileCount)
	}
	if mq.releaseCount != 0 {
		t.Fatalf("ReleaseSequence called %d times, want 0", mq.releaseCount)
	}
	if mq.lastUpdate == nil || mq.lastUpdate.Status != store.StatusQueued {
		t.Fatalf("record must be requeued, got %+v", mq.lastUpdate)
	}
	if mq.lastUpdate.SequenceNumber != nil {
		t.Fatalf("record sequence_number must be cleared, got %d", *mq.lastUpdate.SequenceNumber)
	}
}
