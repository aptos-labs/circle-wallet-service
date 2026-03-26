package txn

import (
	"context"

	aptossdk "github.com/aptos-labs/aptos-go-sdk"
	"github.com/aptos-labs/aptos-go-sdk/api"
	"github.com/aptos-labs/aptos-go-sdk/crypto"

	"github.com/aptos-labs/jc-contract-integration/internal/signer"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
)

// --- mockTxnClient ---

type mockTxnClient struct {
	buildFn  func(aptossdk.AccountAddress, aptossdk.TransactionPayload, uint64) (*aptossdk.RawTransaction, uint64, error)
	submitFn func(*aptossdk.SignedTransaction) (*api.SubmitTransactionResponse, error)
	hashFn   func(string) (*api.Transaction, error)
}

func (m *mockTxnClient) BuildOrderlessTransaction(sender aptossdk.AccountAddress, payload aptossdk.TransactionPayload, maxGasAmount uint64) (*aptossdk.RawTransaction, uint64, error) {
	return m.buildFn(sender, payload, maxGasAmount)
}

func (m *mockTxnClient) SubmitTransaction(signed *aptossdk.SignedTransaction) (*api.SubmitTransactionResponse, error) {
	return m.submitFn(signed)
}

func (m *mockTxnClient) TransactionByHash(hash string) (*api.Transaction, error) {
	return m.hashFn(hash)
}

// --- mockStore ---

type mockStore struct {
	createFn             func(ctx context.Context, rec *store.TransactionRecord) error
	updateFn             func(ctx context.Context, rec *store.TransactionRecord) error
	getFn                func(ctx context.Context, id string) (*store.TransactionRecord, error)
	listStatusFn         func(ctx context.Context, status store.TxnStatus, limit int) ([]*store.TransactionRecord, error)
	listRetryFn          func(ctx context.Context, limit int) ([]*store.TransactionRecord, error)
	listPendingRetriesFn func(ctx context.Context, limit int) ([]*store.TransactionRecord, error)
}

func (m *mockStore) CreateTransaction(ctx context.Context, rec *store.TransactionRecord) error {
	if m.createFn != nil {
		return m.createFn(ctx, rec)
	}
	return nil
}

func (m *mockStore) UpdateTransaction(ctx context.Context, rec *store.TransactionRecord) error {
	if m.updateFn != nil {
		return m.updateFn(ctx, rec)
	}
	return nil
}

func (m *mockStore) GetTransaction(ctx context.Context, id string) (*store.TransactionRecord, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return nil, nil
}

func (m *mockStore) ListByStatus(ctx context.Context, status store.TxnStatus, limit int) ([]*store.TransactionRecord, error) {
	if m.listStatusFn != nil {
		return m.listStatusFn(ctx, status, limit)
	}
	return nil, nil
}

func (m *mockStore) ListRetryable(ctx context.Context, limit int) ([]*store.TransactionRecord, error) {
	if m.listRetryFn != nil {
		return m.listRetryFn(ctx, limit)
	}
	return nil, nil
}

func (m *mockStore) ListPendingRetries(ctx context.Context, limit int) ([]*store.TransactionRecord, error) {
	if m.listPendingRetriesFn != nil {
		return m.listPendingRetriesFn(ctx, limit)
	}
	return nil, nil
}

func (m *mockStore) Close() error { return nil }

// --- mockOperation ---

type mockOperation struct {
	name    string
	role    string
	payload aptossdk.TransactionPayload
	err     error
	json    []byte
}

func (o *mockOperation) Name() string         { return o.name }
func (o *mockOperation) SignerAddress() string { return o.role }
func (o *mockOperation) RequestJSON() []byte  { return o.json }
func (o *mockOperation) BuildPayload() (aptossdk.TransactionPayload, error) {
	return o.payload, o.err
}

// --- mockSigner ---

type mockSigner struct {
	signFn func(ctx context.Context, message []byte) (*crypto.AccountAuthenticator, error)
	pubKey crypto.PublicKey
	addr   aptossdk.AccountAddress
}

func (s *mockSigner) Sign(ctx context.Context, message []byte) (*crypto.AccountAuthenticator, error) {
	return s.signFn(ctx, message)
}

func (s *mockSigner) PublicKey() crypto.PublicKey      { return s.pubKey }
func (s *mockSigner) Address() aptossdk.AccountAddress { return s.addr }

// Ensure mockSigner satisfies signer.Signer.
var _ signer.Signer = (*mockSigner)(nil)

// testRawTxn builds a minimal RawTransaction suitable for unit tests.
// It uses the "empty" sender with a simple entry function payload.
func testRawTxn() *aptossdk.RawTransaction {
	return &aptossdk.RawTransaction{
		Sender: aptossdk.AccountAddress{},
		Payload: aptossdk.TransactionPayload{
			Payload: &aptossdk.EntryFunction{
				Module: aptossdk.ModuleId{
					Address: aptossdk.AccountAddress{},
					Name:    "test",
				},
				Function: "noop",
				ArgTypes: []aptossdk.TypeTag{},
				Args:     [][]byte{},
			},
		},
	}
}
