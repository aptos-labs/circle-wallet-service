package handler

import (
	"context"

	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/aptos-labs/jc-contract-integration/internal/txn"
)

// --- mockSubmitter implements txn.Submitter ---

type mockSubmitter struct {
	submitFn     func(ctx context.Context, op txn.Operation) (string, error)
	getFn        func(ctx context.Context, id string) (*store.TransactionRecord, error)
	submitCalls  int
	submittedOps []txn.Operation
}

func (m *mockSubmitter) Submit(ctx context.Context, op txn.Operation) (string, error) {
	m.submitCalls++
	m.submittedOps = append(m.submittedOps, op)
	if m.submitFn != nil {
		return m.submitFn(ctx, op)
	}
	return "", nil
}

func (m *mockSubmitter) GetTransaction(ctx context.Context, id string) (*store.TransactionRecord, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return nil, nil
}
