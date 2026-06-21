package security

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type encryptedEnvelope struct {
	Data string `json:"data"`
}

/**
 * jsonError.
 *
 * Params:
 *   w http.ResponseWriter - the HTTP response writer the result is written to
 *   code int - the code value
 *   msg string - the msg string
 */
func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"status":"error","message":%q}`, msg)
}

/**
 * DecryptMiddleware transparently decrypts AES-256-GCM request bodies.
 *
 * Callers must send: {"data":"<base64url-AES-GCM-ciphertext>"}
 * The middleware replaces the body with the decrypted plaintext before
 * any handler reads it. Requests with no body (GET, etc.) pass through.
 *
 * If c is nil (ENCRYPTION_KEY not set), the middleware is a no-op.
 *
 * Params:
 *   c *Cipher - the c (*Cipher)
 *
 * Returns:
 *   func(http.Handler) http.Handler - the resulting func(http.Handler) http.Handler
 */
func DecryptMiddleware(c *Cipher) func(http.Handler) http.Handler {
	if c == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body == nil || r.ContentLength == 0 {
				next.ServeHTTP(w, r)
				return
			}

			var env encryptedEnvelope
			if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
				jsonError(w, http.StatusBadRequest, "invalid encrypted payload")
				return
			}
			if env.Data == "" {
				jsonError(w, http.StatusBadRequest, "encrypted payload missing 'data' field")
				return
			}

			plaintext, err := c.Decrypt(env.Data)
			if err != nil {
				jsonError(w, http.StatusBadRequest, "failed to decrypt payload")
				return
			}

			r.Body = io.NopCloser(bytes.NewReader(plaintext))
			r.ContentLength = int64(len(plaintext))
			next.ServeHTTP(w, r)
		})
	}
}
