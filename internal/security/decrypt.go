package security

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

type encryptedEnvelope struct {
	Data string `json:"data"`
}

// DecryptMiddleware transparently decrypts AES-256-GCM request bodies.
//
// Callers must send: {"data":"<base64url-AES-GCM-ciphertext>"}
// The middleware replaces the body with the decrypted plaintext before
// any handler reads it. Requests with no body (GET, etc.) pass through.
//
// If c is nil (ENCRYPTION_KEY not set), the middleware is a no-op.
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
				http.Error(w, `{"status":"error","message":"invalid encrypted payload"}`, http.StatusBadRequest)
				return
			}
			if env.Data == "" {
				http.Error(w, `{"status":"error","message":"encrypted payload missing 'data' field"}`, http.StatusBadRequest)
				return
			}

			plaintext, err := c.Decrypt(env.Data)
			if err != nil {
				http.Error(w, `{"status":"error","message":"failed to decrypt payload"}`, http.StatusBadRequest)
				return
			}

			r.Body = io.NopCloser(bytes.NewReader(plaintext))
			r.ContentLength = int64(len(plaintext))
			next.ServeHTTP(w, r)
		})
	}
}
