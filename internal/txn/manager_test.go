package txn

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
	"github.com/aptos-labs/aptos-go-sdk/crypto"

	"github.com/aptos-labs/jc-contract-integration/internal/account"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

func newTestManager(client TxnClient, st store.Store, registry *account.Registry) *Manager {
	return NewManager(client, st, registry, 3, 60, 100_000, 0, slog.Default())
}

// setupHappyPath wires up a mock client, store, signer, and registry for the
// standard success scenario. Returns the Manager and a pointer to the last
// store record that was updated (captured via the update callback).
func setupHappyPath() (*Manager, *store.TransactionRecord) {
	var captured store.TransactionRecord

	mc := &mockTxnClient{
		buildFn: func(_ aptossdk.AccountAddress, _ aptossdk.TransactionPayload, _ uint64) (*aptossdk.RawTransaction, uint64, error) {
			return testRawTxn(), 42, nil
		},
		submitFn: func(_ *aptossdk.SignedTransaction) (*api.SubmitTransactionResponse, error) {
			return &api.SubmitTransactionResponse{Hash: "0xabc"}, nil
		},
	}

	ms := &mockStore{
		createFn: func(_ context.Context, rec *store.TransactionRecord) error {
			captured = *rec
			return nil
		},
		updateFn: func(_ context.Context, rec *store.TransactionRecord) error {
			captured = *rec
			return nil
		},
	}

	key, _ := crypto.GenerateEd25519PrivateKey()
	sig := &mockSigner{
		signFn: func(_ context.Context, msg []byte) (*crypto.AccountAuthenticator, error) {
			return key.Sign(msg)
		},
		pubKey: key.PubKey(),
		addr:   aptossdk.AccountAddress{},
	}

	reg := account.NewRegistry()
	reg.Register("minter", sig)

	mgr := newTestManager(mc, ms, reg)
	return mgr, &captured
}

func TestSubmit_HappyPath(t *testing.T) {
	mgr, captured := setupHappyPath()

	op := &mockOperation{name: "mint", role: "minter", json: []byte(`{}`)}

	txnID, err := mgr.Submit(context.Background(), op)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txnID == "" {
		t.Fatal("expected non-empty txnID")
	}

	if captured.Status != store.StatusSubmitted {
		t.Errorf("status = %q, want %q", captured.Status, store.StatusSubmitted)
	}
	if captured.TxnHash != "0xabc" {
		t.Errorf("txn_hash = %q, want %q", captured.TxnHash, "0xabc")
	}
}

func TestSubmit_SignerResolveError(t *testing.T) {
	mc := &mockTxnClient{}
	ms := &mockStore{}
	reg := account.NewRegistry() // empty — no signers registered

	mgr := newTestManager(mc, ms, reg)
	op := &mockOperation{name: "mint", role: "minter", json: []byte(`{}`)}

	txnID, err := mgr.Submit(context.Background(), op)
	if err == nil {
		t.Fatal("expected error for missing signer")
	}
	if txnID != "" {
		t.Errorf("txnID = %q, want empty", txnID)
	}
}

func TestSubmit_PayloadBuildError(t *testing.T) {
	mc := &mockTxnClient{}
	ms := &mockStore{}

	key, _ := crypto.GenerateEd25519PrivateKey()
	sig := &mockSigner{
		signFn: func(_ context.Context, _ []byte) (*crypto.AccountAuthenticator, error) {
			return nil, nil
		},
		pubKey: key.PubKey(),
		addr:   aptossdk.AccountAddress{},
	}
	reg := account.NewRegistry()
	reg.Register("minter", sig)

	mgr := newTestManager(mc, ms, reg)
	op := &mockOperation{
		name: "mint",
		role: "minter",
		err:  errors.New("bad payload"),
		json: []byte(`{}`),
	}

	txnID, err := mgr.Submit(context.Background(), op)
	if err == nil {
		t.Fatal("expected error for payload build failure")
	}
	if txnID != "" {
		t.Errorf("txnID = %q, want empty", txnID)
	}
}

func TestSubmit_BuildTxnError(t *testing.T) {
	mc := &mockTxnClient{
		buildFn: func(_ aptossdk.AccountAddress, _ aptossdk.TransactionPayload, _ uint64) (*aptossdk.RawTransaction, uint64, error) {
			return nil, 0, errors.New("build failed")
		},
	}
	ms := &mockStore{}

	key, _ := crypto.GenerateEd25519PrivateKey()
	sig := &mockSigner{
		signFn: func(_ context.Context, _ []byte) (*crypto.AccountAuthenticator, error) {
			return nil, nil
		},
		pubKey: key.PubKey(),
		addr:   aptossdk.AccountAddress{},
	}
	reg := account.NewRegistry()
	reg.Register("minter", sig)

	mgr := newTestManager(mc, ms, reg)
	op := &mockOperation{name: "mint", role: "minter", json: []byte(`{}`)}

	txnID, err := mgr.Submit(context.Background(), op)
	if err == nil {
		t.Fatal("expected error for build txn failure")
	}
	if txnID != "" {
		t.Errorf("txnID = %q, want empty", txnID)
	}
}

func TestSubmit_SignError(t *testing.T) {
	var captured store.TransactionRecord

	mc := &mockTxnClient{
		buildFn: func(_ aptossdk.AccountAddress, _ aptossdk.TransactionPayload, _ uint64) (*aptossdk.RawTransaction, uint64, error) {
			return testRawTxn(), 42, nil
		},
	}
	ms := &mockStore{
		createFn: func(_ context.Context, rec *store.TransactionRecord) error {
			captured = *rec
			return nil
		},
		updateFn: func(_ context.Context, rec *store.TransactionRecord) error {
			captured = *rec
			return nil
		},
	}

	key, _ := crypto.GenerateEd25519PrivateKey()
	sig := &mockSigner{
		signFn: func(_ context.Context, _ []byte) (*crypto.AccountAuthenticator, error) {
			return nil, errors.New("sign failed")
		},
		pubKey: key.PubKey(),
		addr:   aptossdk.AccountAddress{},
	}
	reg := account.NewRegistry()
	reg.Register("minter", sig)

	mgr := newTestManager(mc, ms, reg)
	op := &mockOperation{name: "mint", role: "minter", json: []byte(`{}`)}

	txnID, err := mgr.Submit(context.Background(), op)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txnID == "" {
		t.Fatal("expected non-empty txnID")
	}
	if captured.Status != store.StatusFailed {
		t.Errorf("status = %q, want %q", captured.Status, store.StatusFailed)
	}
}

func TestSubmit_SubmitError(t *testing.T) {
	var captured store.TransactionRecord

	mc := &mockTxnClient{
		buildFn: func(_ aptossdk.AccountAddress, _ aptossdk.TransactionPayload, _ uint64) (*aptossdk.RawTransaction, uint64, error) {
			return testRawTxn(), 42, nil
		},
		submitFn: func(_ *aptossdk.SignedTransaction) (*api.SubmitTransactionResponse, error) {
			return nil, errors.New("submit failed")
		},
	}
	ms := &mockStore{
		createFn: func(_ context.Context, rec *store.TransactionRecord) error {
			captured = *rec
			return nil
		},
		updateFn: func(_ context.Context, rec *store.TransactionRecord) error {
			captured = *rec
			return nil
		},
	}

	key, _ := crypto.GenerateEd25519PrivateKey()
	sig := &mockSigner{
		signFn: func(_ context.Context, msg []byte) (*crypto.AccountAuthenticator, error) {
			return key.Sign(msg)
		},
		pubKey: key.PubKey(),
		addr:   aptossdk.AccountAddress{},
	}
	reg := account.NewRegistry()
	reg.Register("minter", sig)

	mgr := newTestManager(mc, ms, reg)
	op := &mockOperation{name: "mint", role: "minter", json: []byte(`{}`)}

	txnID, err := mgr.Submit(context.Background(), op)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txnID == "" {
		t.Fatal("expected non-empty txnID")
	}
	if captured.Status != store.StatusFailed {
		t.Errorf("status = %q, want %q", captured.Status, store.StatusFailed)
	}
}

func TestGetTransaction(t *testing.T) {
	expected := &store.TransactionRecord{ID: "abc-123", Status: store.StatusConfirmed}
	ms := &mockStore{
		getFn: func(_ context.Context, id string) (*store.TransactionRecord, error) {
			if id == "abc-123" {
				return expected, nil
			}
			return nil, nil
		},
	}

	mgr := newTestManager(&mockTxnClient{}, ms, account.NewRegistry())

	rec, err := mgr.GetTransaction(context.Background(), "abc-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec == nil || rec.ID != "abc-123" {
		t.Errorf("expected record with ID abc-123, got %v", rec)
	}

	rec2, err := mgr.GetTransaction(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec2 != nil {
		t.Errorf("expected nil for nonexistent, got %v", rec2)
	}
}
