package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	aptosint "github.com/aptos-labs/jc-contract-integration/internal/aptos"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/aptos-labs/jc-contract-integration/internal/txn"
)

// testAddr is a valid 64-char hex Aptos address for testing.
const testAddr = "0x0000000000000000000000000000000000000000000000000000000000000001"

// --- Helpers ---

func newSubmitter() *mockSubmitter {
	return &mockSubmitter{
		submitFn: func(_ context.Context, _ txn.Operation) (string, error) {
			return "txn-123", nil
		},
		getFn: func(_ context.Context, _ string) (*store.TransactionRecord, error) {
			return nil, nil
		},
	}
}

func postJSON(t *testing.T, handler http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func getRequest(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func assertStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Errorf("status = %d, want %d; body = %s", w.Code, want, w.Body.String())
	}
}

func assertJSONField(t *testing.T, w *httptest.ResponseRecorder, key string, want any) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	got, ok := m[key]
	if !ok {
		t.Errorf("response missing key %q", key)
		return
	}
	switch v := want.(type) {
	case int:
		if got != float64(v) {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	case uint64:
		if got != float64(v) {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	default:
		if got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
}

func assertSubmitCalls(t *testing.T, mgr *mockSubmitter, want int) {
	t.Helper()
	if mgr.submitCalls != want {
		t.Fatalf("submit calls = %d, want %d", mgr.submitCalls, want)
	}
}

func mustSubmittedOp(t *testing.T, mgr *mockSubmitter) txn.Operation {
	t.Helper()
	if len(mgr.submittedOps) == 0 {
		t.Fatal("expected at least one submitted operation")
	}
	op := mgr.submittedOps[len(mgr.submittedOps)-1]
	if op == nil {
		t.Fatal("expected non-nil submitted operation")
	}
	return op
}

func assertOpMeta(t *testing.T, op txn.Operation, wantName, wantRole string) {
	t.Helper()
	if got := op.Name(); got != wantName {
		t.Fatalf("op.Name() = %q, want %q", got, wantName)
	}
	if got := op.RequiredRole(); got != wantRole {
		t.Fatalf("op.RequiredRole() = %q, want %q", got, wantRole)
	}
}

// --- Execute handler tests ---

// mockABIServer starts a test HTTP server that serves module ABI responses.
func mockABIServer(t *testing.T, params []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		abiResp := map[string]any{
			"abi": map[string]any{
				"exposed_functions": []map[string]any{
					{
						"name":                "mint",
						"params":              append([]string{"&signer"}, params...),
						"is_entry":            true,
						"is_view":             false,
						"generic_type_params": []any{},
					},
					{
						"name":                "balance_of",
						"params":              []string{"address"},
						"is_entry":            false,
						"is_view":             true,
						"generic_type_params": []any{},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(abiResp)
	}))
}

func TestExecute_MissingFunctionID(t *testing.T) {
	h := Execute(nil, nil)
	w := postJSON(t, h, "/v1/contracts/execute", `{"signer":"minter","arguments":[]}`)
	assertStatus(t, w, http.StatusBadRequest)
}

func TestExecute_MissingSigner(t *testing.T) {
	h := Execute(nil, nil)
	w := postJSON(t, h, "/v1/contracts/execute", `{"function_id":"0x1::mod::func","arguments":[]}`)
	assertStatus(t, w, http.StatusBadRequest)
}

func TestExecute_InvalidFunctionID(t *testing.T) {
	h := Execute(nil, nil)
	w := postJSON(t, h, "/v1/contracts/execute", `{"function_id":"bad","signer":"minter"}`)
	assertStatus(t, w, http.StatusBadRequest)
}

func TestExecute_Success(t *testing.T) {
	server := mockABIServer(t, []string{"address", "u64"})
	defer server.Close()

	abiCache := aptosint.NewABICache(server.URL)
	mgr := newSubmitter()

	h := Execute(mgr, abiCache)
	body := fmt.Sprintf(`{
		"function_id": "%s::contractInt::mint",
		"type_arguments": [],
		"arguments": ["%s", "10000"],
		"signer": "minter"
	}`, testAddr, testAddr)
	w := postJSON(t, h, "/v1/contracts/execute", body)
	assertStatus(t, w, http.StatusAccepted)
	assertJSONField(t, w, "transaction_id", "txn-123")
	assertSubmitCalls(t, mgr, 1)

	op := mustSubmittedOp(t, mgr)
	assertOpMeta(t, op, "execute", "minter")
}

func TestExecute_WrongArgCount(t *testing.T) {
	server := mockABIServer(t, []string{"address", "u64"})
	defer server.Close()

	abiCache := aptosint.NewABICache(server.URL)

	h := Execute(nil, abiCache)
	body := fmt.Sprintf(`{
		"function_id": "%s::contractInt::mint",
		"arguments": ["%s"],
		"signer": "minter"
	}`, testAddr, testAddr)
	w := postJSON(t, h, "/v1/contracts/execute", body)
	assertStatus(t, w, http.StatusBadRequest)
}

func TestExecute_SubmitError(t *testing.T) {
	server := mockABIServer(t, []string{"address", "u64"})
	defer server.Close()

	abiCache := aptosint.NewABICache(server.URL)
	mgr := &mockSubmitter{
		submitFn: func(_ context.Context, _ txn.Operation) (string, error) {
			return "", errors.New("submit error")
		},
	}

	h := Execute(mgr, abiCache)
	body := fmt.Sprintf(`{
		"function_id": "%s::contractInt::mint",
		"arguments": ["%s", "10000"],
		"signer": "minter"
	}`, testAddr, testAddr)
	w := postJSON(t, h, "/v1/contracts/execute", body)
	assertStatus(t, w, http.StatusInternalServerError)
}

// --- Query handler tests ---

func TestQuery_MissingFunctionID(t *testing.T) {
	h := Query("http://localhost:1234")
	w := postJSON(t, h, "/v1/contracts/query", `{"arguments":[]}`)
	assertStatus(t, w, http.StatusBadRequest)
}

func TestQuery_Success(t *testing.T) {
	aptosNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/view" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`["10000"]`))
	}))
	defer aptosNode.Close()

	h := Query(aptosNode.URL)
	body := fmt.Sprintf(`{
		"function_id": "%s::contractInt::balance_of",
		"type_arguments": [],
		"arguments": ["%s"]
	}`, testAddr, testAddr)
	w := postJSON(t, h, "/v1/contracts/query", body)
	assertStatus(t, w, http.StatusOK)

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if string(resp["result"]) != `["10000"]` {
		t.Errorf("result = %s, want [\"10000\"]", string(resp["result"]))
	}
}

func TestQuery_AptosNodeError(t *testing.T) {
	aptosNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"function not found"}`))
	}))
	defer aptosNode.Close()

	h := Query(aptosNode.URL)
	body := fmt.Sprintf(`{"function_id": "%s::contractInt::nonexistent"}`, testAddr)
	w := postJSON(t, h, "/v1/contracts/query", body)
	assertStatus(t, w, http.StatusBadRequest)
}

// --- Transaction tracking handler tests ---

func TestGetTransaction_Found(t *testing.T) {
	mgr := &mockSubmitter{
		getFn: func(_ context.Context, id string) (*store.TransactionRecord, error) {
			return &store.TransactionRecord{ID: id, Status: store.StatusConfirmed}, nil
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/transactions/{id}", GetTransaction(mgr))

	w := getRequest(t, mux, "/v1/transactions/abc-123")
	assertStatus(t, w, http.StatusOK)
	assertJSONField(t, w, "id", "abc-123")
}

func TestGetTransaction_NotFound(t *testing.T) {
	mgr := &mockSubmitter{
		getFn: func(_ context.Context, id string) (*store.TransactionRecord, error) {
			return nil, nil
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/transactions/{id}", GetTransaction(mgr))

	w := getRequest(t, mux, "/v1/transactions/nonexistent")
	assertStatus(t, w, http.StatusNotFound)
}

func TestGetTransaction_MissingID(t *testing.T) {
	mgr := &mockSubmitter{
		getFn: func(_ context.Context, id string) (*store.TransactionRecord, error) {
			return nil, nil
		},
	}

	h := GetTransaction(mgr)
	req := httptest.NewRequest(http.MethodGet, "/v1/transactions/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assertStatus(t, w, http.StatusBadRequest)
}

func TestGetTransaction_StoreError(t *testing.T) {
	mgr := &mockSubmitter{
		getFn: func(_ context.Context, _ string) (*store.TransactionRecord, error) {
			return nil, errors.New("db error")
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/transactions/{id}", GetTransaction(mgr))

	w := getRequest(t, mux, "/v1/transactions/abc-123")
	assertStatus(t, w, http.StatusInternalServerError)
}
