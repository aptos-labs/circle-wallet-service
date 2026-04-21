package handler

import (
	"encoding/json"
	"net/http"

	"golang.org/x/time/rate"
)

// RateLimiterConfig controls the optional upstream rate limiter.
//
// Per-wallet limiting is not yet implemented — the middleware enforces a
// single global token bucket. Config.validate rejects rate_limit.per_wallet=true
// at startup so the absence of a PerWallet field here can't silently mislead
// operators.
type RateLimiterConfig struct {
	Enabled           bool
	RequestsPerSecond int
	Burst             int
}

// RateLimitMiddleware wraps an http.Handler with a global token-bucket rate
// limiter. When the bucket is empty it returns 429 with a Retry-After header.
type RateLimitMiddleware struct {
	global  *rate.Limiter
	enabled bool
}

func NewRateLimitMiddleware(cfg RateLimiterConfig) *RateLimitMiddleware {
	rps := cfg.RequestsPerSecond
	if rps < 1 {
		rps = 1
	}
	burst := cfg.Burst
	if burst < 1 {
		burst = 1
	}
	return &RateLimitMiddleware{
		global:  rate.NewLimiter(rate.Limit(rps), burst),
		enabled: cfg.Enabled,
	}
}

func (rl *RateLimitMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.enabled {
			next.ServeHTTP(w, r)
			return
		}
		if !rl.global.Allow() {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
