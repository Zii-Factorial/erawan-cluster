package security

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

const apiKeyHeader = "X-API-Key"

/**
 * APIKeyMiddleware.
 *
 * Params:
 *   apiKey string - the apiKey string
 *
 * Returns:
 *   func(http.Handler) http.Handler - the resulting func(http.Handler) http.Handler
 */
func APIKeyMiddleware(apiKey string) func(http.Handler) http.Handler {
	if strings.TrimSpace(apiKey) == "" {
		return func(next http.Handler) http.Handler {
			return next
		}
	}

	expected := []byte(apiKey)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := []byte(r.Header.Get(apiKeyHeader))
			if len(provided) == 0 || subtle.ConstantTimeCompare(expected, provided) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
