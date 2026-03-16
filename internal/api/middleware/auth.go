package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/aptos-labs/jc-contract-integration/internal/api"
)

// Auth validates the API key from the Authorization header.
func Auth(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Get("Authorization")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(apiKey)) != 1 {
				api.Error(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
