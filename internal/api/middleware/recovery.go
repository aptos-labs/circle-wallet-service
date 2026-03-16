package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/aptos-labs/jc-contract-integration/internal/api"
)

// Recovery recovers from panics and returns a 500 error.
func Recovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					logger.Error("panic recovered",
						"error", err,
						"stack", string(debug.Stack()),
						"request_id", GetRequestID(r.Context()),
					)
					api.Error(w, http.StatusInternalServerError, "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
