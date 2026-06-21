// Package security_test holds black-box unit tests for internal/security.
package security_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"erawan-cluster/internal/security"
)

const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // 64 hex / 32 bytes

func TestCipherRoundTrip(t *testing.T) {
	c, err := security.NewCipher(testKey)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	plaintext := []byte(`{"secret":"value"}`)
	enc, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if enc == string(plaintext) {
		t.Fatal("ciphertext should differ from plaintext")
	}
	dec, err := c.Decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(dec) != string(plaintext) {
		t.Fatalf("round-trip mismatch: %q", dec)
	}
}

func TestCipherRejectsBadKey(t *testing.T) {
	if _, err := security.NewCipher("tooshort"); err == nil {
		t.Fatal("expected error for non-hex / wrong-length key")
	}
}

func TestCipherDecryptRejectsTampered(t *testing.T) {
	c, _ := security.NewCipher(testKey)
	if _, err := c.Decrypt("not-valid-base64!!"); err == nil {
		t.Fatal("expected decrypt to reject malformed ciphertext")
	}
}

func TestValidateAuthConfig(t *testing.T) {
	if err := security.ValidateAuthConfig("production", ""); err == nil {
		t.Fatal("expected fail-closed without API key outside dev")
	}
	if err := security.ValidateAuthConfig("dev", ""); err != nil {
		t.Fatalf("expected dev to allow empty key, got %v", err)
	}
	if err := security.ValidateAuthConfig("production", "secret"); err != nil {
		t.Fatalf("expected production with key to pass, got %v", err)
	}
}

func TestAPIKeyMiddlewareEnforcesKey(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := security.APIKeyMiddleware("topsecret")(ok)

	// No key -> 401
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", rr.Code)
	}

	// Correct key -> 200
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "topsecret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct key, got %d", rr.Code)
	}

	// Wrong key -> 401
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "wrong")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong key, got %d", rr.Code)
	}
}

func TestAPIKeyMiddlewareDisabledWhenEmpty(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := security.APIKeyMiddleware("")(ok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected pass-through (200) when no key configured, got %d", rr.Code)
	}
}
