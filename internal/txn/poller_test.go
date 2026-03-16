package txn

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/aptos-labs/aptos-go-sdk/api"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

func TestCheckSubmitted_Confirmed(t *testing.T) {
	var updated store.TransactionRecord

	mc := &mockTxnClient{
		hashFn: func(hash string) (*api.Transaction, error) {
			return &api.Transaction{
				Inner: &api.UserTransaction{
					Success: true,
				},
			}, nil
		},
	}
	ms := &mockStore{
		listStatusFn: func(_ context.Context, status store.TxnStatus, _ int) ([]*store.TransactionRecord, error) {
			if status == store.StatusSubmitted {
				return []*store.TransactionRecord{
					{ID: "tx1", Status: store.StatusSubmitted, TxnHash: "0xabc", ExpiresAt: time.Now().Add(time.Hour)},
				}, nil
			}
			return nil, nil
		},
		updateFn: func(_ context.Context, rec *store.TransactionRecord) error {
			updated = *rec
			return nil
		},
	}

	p := NewPoller(mc, ms, time.Second, 60, 10, 300, nil, slog.Default())
	p.checkSubmitted(context.Background())

	if updated.Status != store.StatusConfirmed {
		t.Errorf("status = %q, want %q", updated.Status, store.StatusConfirmed)
	}
}

func TestCheckSubmitted_FailedOnChain(t *testing.T) {
	var updated store.TransactionRecord

	mc := &mockTxnClient{
		hashFn: func(hash string) (*api.Transaction, error) {
			return &api.Transaction{
				Inner: &api.UserTransaction{
					Success:  false,
					VmStatus: "MOVE_ABORT",
				},
			}, nil
		},
	}
	ms := &mockStore{
		listStatusFn: func(_ context.Context, status store.TxnStatus, _ int) ([]*store.TransactionRecord, error) {
			if status == store.StatusSubmitted {
				return []*store.TransactionRecord{
					{ID: "tx1", Status: store.StatusSubmitted, TxnHash: "0xabc", ExpiresAt: time.Now().Add(time.Hour)},
				}, nil
			}
			return nil, nil
		},
		updateFn: func(_ context.Context, rec *store.TransactionRecord) error {
			updated = *rec
			return nil
		},
	}

	p := NewPoller(mc, ms, time.Second, 60, 10, 300, nil, slog.Default())
	p.checkSubmitted(context.Background())

	if updated.Status != store.StatusFailed {
		t.Errorf("status = %q, want %q", updated.Status, store.StatusFailed)
	}
	if updated.ErrorMessage != "MOVE_ABORT" {
		t.Errorf("error_message = %q, want %q", updated.ErrorMessage, "MOVE_ABORT")
	}
}

func TestCheckSubmitted_Expired(t *testing.T) {
	var updated store.TransactionRecord

	mc := &mockTxnClient{
		hashFn: func(hash string) (*api.Transaction, error) {
			return nil, errors.New("not called")
		},
	}
	ms := &mockStore{
		listStatusFn: func(_ context.Context, status store.TxnStatus, _ int) ([]*store.TransactionRecord, error) {
			if status == store.StatusSubmitted {
				return []*store.TransactionRecord{
					{ID: "tx1", Status: store.StatusSubmitted, TxnHash: "0xabc", ExpiresAt: time.Now().Add(-time.Minute)},
				}, nil
			}
			return nil, nil
		},
		updateFn: func(_ context.Context, rec *store.TransactionRecord) error {
			updated = *rec
			return nil
		},
	}

	p := NewPoller(mc, ms, time.Second, 60, 10, 300, nil, slog.Default())
	p.checkSubmitted(context.Background())

	if updated.Status != store.StatusExpired {
		t.Errorf("status = %q, want %q", updated.Status, store.StatusExpired)
	}
}

func TestCheckSubmitted_NotFound(t *testing.T) {
	updateCalled := false

	mc := &mockTxnClient{
		hashFn: func(hash string) (*api.Transaction, error) {
			return nil, errors.New("404 not found")
		},
	}
	ms := &mockStore{
		listStatusFn: func(_ context.Context, status store.TxnStatus, _ int) ([]*store.TransactionRecord, error) {
			if status == store.StatusSubmitted {
				return []*store.TransactionRecord{
					{ID: "tx1", Status: store.StatusSubmitted, TxnHash: "0xabc", ExpiresAt: time.Now().Add(time.Hour)},
				}, nil
			}
			return nil, nil
		},
		updateFn: func(_ context.Context, rec *store.TransactionRecord) error {
			updateCalled = true
			return nil
		},
	}

	p := NewPoller(mc, ms, time.Second, 60, 10, 300, nil, slog.Default())
	p.checkSubmitted(context.Background())

	if updateCalled {
		t.Error("expected no update for not-found transaction")
	}
}

func TestCheckSubmitted_EmptyHash(t *testing.T) {
	hashCalled := false

	mc := &mockTxnClient{
		hashFn: func(hash string) (*api.Transaction, error) {
			hashCalled = true
			return nil, nil
		},
	}
	ms := &mockStore{
		listStatusFn: func(_ context.Context, status store.TxnStatus, _ int) ([]*store.TransactionRecord, error) {
			if status == store.StatusSubmitted {
				return []*store.TransactionRecord{
					{ID: "tx1", Status: store.StatusSubmitted, TxnHash: "", ExpiresAt: time.Now().Add(time.Hour)},
				}, nil
			}
			return nil, nil
		},
	}

	p := NewPoller(mc, ms, time.Second, 60, 10, 300, nil, slog.Default())
	p.checkSubmitted(context.Background())

	if hashCalled {
		t.Error("TransactionByHash should not be called for empty hash")
	}
}

func TestProcessRetryable_Retry(t *testing.T) {
	var updated store.TransactionRecord

	mc := &mockTxnClient{}
	ms := &mockStore{
		listRetryFn: func(_ context.Context, _ int) ([]*store.TransactionRecord, error) {
			return []*store.TransactionRecord{
				{ID: "tx1", Status: store.StatusFailed, Attempt: 0, MaxRetries: 3},
			}, nil
		},
		updateFn: func(_ context.Context, rec *store.TransactionRecord) error {
			updated = *rec
			return nil
		},
	}

	p := NewPoller(mc, ms, time.Second, 60, 10, 300, nil, slog.Default())
	p.processRetryable(context.Background())

	if updated.Status != store.StatusPending {
		t.Errorf("status = %q, want %q", updated.Status, store.StatusPending)
	}
	if updated.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", updated.Attempt)
	}
}

func TestProcessRetryable_PermanentlyFailed(t *testing.T) {
	var updated store.TransactionRecord

	mc := &mockTxnClient{}
	ms := &mockStore{
		listRetryFn: func(_ context.Context, _ int) ([]*store.TransactionRecord, error) {
			return []*store.TransactionRecord{
				{ID: "tx1", Status: store.StatusFailed, Attempt: 3, MaxRetries: 3},
			}, nil
		},
		updateFn: func(_ context.Context, rec *store.TransactionRecord) error {
			updated = *rec
			return nil
		},
	}

	p := NewPoller(mc, ms, time.Second, 60, 10, 300, nil, slog.Default())
	p.processRetryable(context.Background())

	if updated.Status != store.StatusPermanentlyFailed {
		t.Errorf("status = %q, want %q", updated.Status, store.StatusPermanentlyFailed)
	}
}

func TestRun_ContextCancel(t *testing.T) {
	mc := &mockTxnClient{}
	ms := &mockStore{}

	p := NewPoller(mc, ms, 50*time.Millisecond, 60, 10, 300, nil, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		err  string
		want bool
	}{
		{"404 not found", true},
		{"transaction not found", true},
		{"server error 500", false},
		{"connection refused", false},
	}

	for _, tc := range tests {
		t.Run(tc.err, func(t *testing.T) {
			got := isNotFound(errors.New(tc.err))
			if got != tc.want {
				t.Errorf("isNotFound(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestVmStatusFromTxn(t *testing.T) {
	t.Run("UserTransaction", func(t *testing.T) {
		txn := &api.Transaction{
			Inner: &api.UserTransaction{VmStatus: "OUT_OF_GAS"},
		}
		got := vmStatusFromTxn(txn)
		if got != "OUT_OF_GAS" {
			t.Errorf("vmStatusFromTxn = %q, want %q", got, "OUT_OF_GAS")
		}
	})

	t.Run("nil inner", func(t *testing.T) {
		txn := &api.Transaction{Inner: nil}
		got := vmStatusFromTxn(txn)
		if got != "unknown" {
			t.Errorf("vmStatusFromTxn = %q, want %q", got, "unknown")
		}
	})
}
