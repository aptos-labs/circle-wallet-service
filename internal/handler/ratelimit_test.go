package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRateLimit_AllowedUnderLimit(t *testing.T) {
	rl := NewRateLimitMiddleware(RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 100,
		Burst:             10,
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := rl.Wrap(next)
	for i := 0; i < 5; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: want 200 got %d", i, rr.Code)
		}
	}
}

func TestRateLimit_BlockedOverLimit(t *testing.T) {
	rl := NewRateLimitMiddleware(RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 1000,
		Burst:             1,
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := rl.Wrap(next)
	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr1.Code != http.StatusOK {
		t.Fatalf("first: %d", rr1.Code)
	}
	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("request %d: want 429 got %d body %s", i, rr.Code, rr.Body.String())
		}
	}
}

func TestRateLimit_RetryAfterHeader(t *testing.T) {
	rl := NewRateLimitMiddleware(RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 1000,
		Burst:             1,
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := rl.Wrap(next)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 got %d", rr.Code)
	}
	ra := rr.Header().Get("Retry-After")
	if ra == "" {
		t.Fatal("missing Retry-After")
	}
}

func TestRateLimit_DefaultClampsRPSAndBurst(t *testing.T) {
	rl := NewRateLimitMiddleware(RateLimiterConfig{
		Enabled:           true,
		RequestsPerSecond: 0,
		Burst:             0,
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := rl.Wrap(next)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("first request: %d", rr.Code)
	}
}

func TestRateLimit_DisabledPassesThrough(t *testing.T) {
	rl := NewRateLimitMiddleware(RateLimiterConfig{
		Enabled:           false,
		RequestsPerSecond: 1,
		Burst:             1,
	})
	var hits int
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	h := rl.Wrap(next)
	for i := 0; i < 20; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: want 200 got %d", i, rr.Code)
		}
	}
	if hits != 20 {
		t.Fatalf("hits=%d", hits)
	}
}
