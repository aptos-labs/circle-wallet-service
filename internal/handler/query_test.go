package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aptos-labs/jc-contract-integration/internal/aptos"
)

func TestQueryMissingFunctionID(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	cli, err := aptos.NewClient(srv.URL+"/v1", 2, 3600, 2000000, "")
	if err != nil {
		t.Fatal(err)
	}
	cache := aptos.NewABICache(cli.Inner)
	h := Query(cli, cache)

	body := map[string]any{"arguments": []any{}}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/query", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestQueryValidRequest(t *testing.T) {
	moduleJSON := []byte(`{
  "bytecode": "0x",
  "abi": {
    "address": "0x1",
    "name": "m",
    "friends": [],
    "exposed_functions": [
      {
        "name": "get_x",
        "visibility": "public",
        "is_entry": false,
        "is_view": true,
        "generic_type_params": [],
        "params": [],
        "return": ["u64"]
      }
    ],
    "structs": []
  }
}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/module/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(moduleJSON)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/view"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[42]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cli, err := aptos.NewClient(srv.URL+"/v1", 2, 3600, 2000000, "")
	if err != nil {
		t.Fatal(err)
	}
	cache := aptos.NewABICache(cli.Inner)
	h := Query(cli, cache)

	body := map[string]any{
		"function_id":    "0x1::m::get_x",
		"type_arguments": []string{},
		"arguments":      []any{},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/query", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	res, ok := resp["result"].([]any)
	if !ok || len(res) != 1 {
		t.Fatalf("result: %#v", resp["result"])
	}
	if n, ok := res[0].(float64); !ok || n != 42 {
		t.Fatalf("first value: %#v", res[0])
	}
}

func TestQueryInvalidFunctionID(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	cli, err := aptos.NewClient(srv.URL+"/v1", 2, 3600, 2000000, "")
	if err != nil {
		t.Fatal(err)
	}
	cache := aptos.NewABICache(cli.Inner)
	h := Query(cli, cache)

	body := map[string]any{"function_id": "not-a-function-id", "arguments": []any{}}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/query", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestQueryArgumentCountMismatch(t *testing.T) {
	moduleJSON := []byte(`{
  "bytecode": "0x",
  "abi": {
    "address": "0x1",
    "name": "m",
    "friends": [],
    "exposed_functions": [
      {
        "name": "f1",
        "visibility": "public",
        "is_entry": false,
        "is_view": true,
        "generic_type_params": [],
        "params": ["u64"],
        "return": []
      }
    ],
    "structs": []
  }
}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/module/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(moduleJSON)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cli, err := aptos.NewClient(srv.URL+"/v1", 2, 3600, 2000000, "")
	if err != nil {
		t.Fatal(err)
	}
	cache := aptos.NewABICache(cli.Inner)
	h := Query(cli, cache)

	body := map[string]any{"function_id": "0x1::m::f1", "arguments": []any{}}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/query", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestQueryViewHTTPError(t *testing.T) {
	moduleJSON := []byte(`{
  "bytecode": "0x",
  "abi": {
    "address": "0x1",
    "name": "m",
    "friends": [],
    "exposed_functions": [
      {
        "name": "v2",
        "visibility": "public",
        "is_entry": false,
        "is_view": true,
        "generic_type_params": [],
        "params": [],
        "return": ["u64"]
      }
    ],
    "structs": []
  }
}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/module/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(moduleJSON)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/view"):
			http.Error(w, "bad", http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cli, err := aptos.NewClient(srv.URL+"/v1", 2, 3600, 2000000, "")
	if err != nil {
		t.Fatal(err)
	}
	cache := aptos.NewABICache(cli.Inner)
	h := Query(cli, cache)

	body := map[string]any{"function_id": "0x1::m::v2", "arguments": []any{}}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/query", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("want 502 got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestQueryABIFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/module/") {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cli, err := aptos.NewClient(srv.URL+"/v1", 2, 3600, 2000000, "")
	if err != nil {
		t.Fatal(err)
	}
	cache := aptos.NewABICache(cli.Inner)
	h := Query(cli, cache)

	body := map[string]any{"function_id": "0x1::missing::f", "arguments": []any{}}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/query", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d: %s", rr.Code, rr.Body.String())
	}
}
