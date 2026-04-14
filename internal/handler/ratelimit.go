package handler

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"

	"golang.org/x/time/rate"
)

type RateLimiterConfig struct {
	Enabled           bool
	RequestsPerSecond int
	Burst             int
	PerWallet         bool // TODO: per-wallet limiting (not implemented; global only).
}

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
			rsv := rl.global.Reserve()
			sec := int(math.Ceil(rsv.Delay().Seconds()))
			if sec < 1 {
				sec = 1
			}
			rsv.Cancel()
			w.Header().Set("Retry-After", strconv.Itoa(sec))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
