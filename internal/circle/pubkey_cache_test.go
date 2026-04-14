package circle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func testClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{
		apiKey:     "test-api-key",
		baseURL:    srv.URL,
		httpClient: srv.Client(),
	}
}

func TestPublicKeyCache_Resolve(t *testing.T) {
	t.Parallel()

	t.Run("CacheHit", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/wallets/") {
				http.NotFound(w, r)
				return
			}
			calls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"wallet":{"id":"w1","address":"a","initialPublicKey":"0xabc123"}}}`))
		}))
		defer srv.Close()

		cache := NewPublicKeyCache(testClient(t, srv))
		ctx := context.Background()
		pk1, err := cache.Resolve(ctx, "w1")
		if err != nil {
			t.Fatal(err)
		}
		pk2, err := cache.Resolve(ctx, "w1")
		if err != nil {
			t.Fatal(err)
		}
		if pk1 != pk2 || pk1 != "0xabc123" {
			t.Fatalf("keys: %q %q", pk1, pk2)
		}
		if calls.Load() != 1 {
			t.Fatalf("HTTP calls: got %d want 1", calls.Load())
		}
	})

	t.Run("CacheMiss", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"wallet":{"id":"wx","address":"a","initialPublicKey":"beef"}}}`))
		}))
		defer srv.Close()

		cache := NewPublicKeyCache(testClient(t, srv))
		pk, err := cache.Resolve(context.Background(), "wx")
		if err != nil {
			t.Fatal(err)
		}
		if pk != "0xbeef" {
			t.Fatalf("got %q want 0xbeef", pk)
		}
		if calls.Load() != 1 {
			t.Fatalf("HTTP calls: got %d want 1", calls.Load())
		}
	})

	t.Run("NoPublicKey", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data":{"wallet":{"id":"w0","address":"a","initialPublicKey":""}}}`))
		}))
		defer srv.Close()

		cache := NewPublicKeyCache(testClient(t, srv))
		_, err := cache.Resolve(context.Background(), "w0")
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "no initialPublicKey") {
			t.Fatalf("error: %v", err)
		}
	})

	t.Run("SetOverride", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}))
		defer srv.Close()

		cache := NewPublicKeyCache(testClient(t, srv))
		cache.Set("w1", "abc")
		pk, err := cache.Resolve(context.Background(), "w1")
		if err != nil {
			t.Fatal(err)
		}
		if pk != "0xabc" {
			t.Fatalf("got %q want 0xabc", pk)
		}
		if calls.Load() != 0 {
			t.Fatalf("HTTP calls: got %d want 0", calls.Load())
		}
	})

	t.Run("Concurrent", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			_, _ = w.Write([]byte(`{"data":{"wallet":{"id":"wc","address":"a","initialPublicKey":"0xcc"}}}`))
		}))
		defer srv.Close()

		cache := NewPublicKeyCache(testClient(t, srv))
		ctx := context.Background()
		var wg sync.WaitGroup
		var mu sync.Mutex
		var resolveErr error
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := cache.Resolve(ctx, "wc")
				if err != nil {
					mu.Lock()
					if resolveErr == nil {
						resolveErr = err
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		n := calls.Load()
		if n < 1 || n > 2 {
			t.Fatalf("HTTP calls: got %d want 1..2", n)
		}
	})
}
