package security

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// maxEncryptBufBytes is the largest response body we will buffer for encryption.
// Responses larger than this are written through to the client unencrypted.
const maxEncryptBufBytes = 16 << 20 // 16 MB

type bufResponseWriter struct {
	inner    http.ResponseWriter
	header   http.Header
	body     bytes.Buffer
	code     int
	overflow bool // true once body exceeded maxEncryptBufBytes; response written through
}

func newBufResponseWriter(w http.ResponseWriter) *bufResponseWriter {
	return &bufResponseWriter{inner: w, header: make(http.Header), code: http.StatusOK}
}

func (b *bufResponseWriter) Header() http.Header { return b.header }

func (b *bufResponseWriter) WriteHeader(code int) {
	if !b.overflow {
		b.code = code
	}
}

func (b *bufResponseWriter) Write(p []byte) (int, error) {
	if b.overflow {
		return b.inner.Write(p)
	}
	if b.body.Len()+len(p) > maxEncryptBufBytes {
		b.overflow = true
		b.flushThrough()
		return b.inner.Write(p)
	}
	return b.body.Write(p)
}

// flushThrough writes the buffered headers, status, and body directly to inner.
// Called when the response overflows the encrypt buffer.
func (b *bufResponseWriter) flushThrough() {
	for k, vs := range b.header {
		for _, v := range vs {
			b.inner.Header().Add(k, v)
		}
	}
	b.inner.WriteHeader(b.code)
	_, _ = b.inner.Write(b.body.Bytes())
	b.body.Reset()
}

// flush writes the buffered response to w (the underlying response writer).
func (b *bufResponseWriter) flush(w http.ResponseWriter) {
	for k, vs := range b.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(b.code)
	_, _ = w.Write(b.body.Bytes())
}

// EncryptMiddleware encrypts all JSON responses with AES-256-GCM when a cipher is configured.
// Non-JSON responses (e.g. zip downloads) and responses larger than maxEncryptBufBytes
// pass through unchanged. If c is nil, the middleware is a no-op.
func EncryptMiddleware(c *Cipher) func(http.Handler) http.Handler {
	if c == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := newBufResponseWriter(w)
			next.ServeHTTP(buf, r)

			if buf.overflow {
				return
			}

			ct := buf.header.Get("Content-Type")
			if !strings.Contains(ct, "application/json") {
				buf.flush(w)
				return
			}

			encrypted, err := c.Encrypt(buf.body.Bytes())
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"status":"error","message":"failed to encrypt response"}`))
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(buf.code)
			_ = json.NewEncoder(w).Encode(map[string]string{"data": encrypted})
		})
	}
}
