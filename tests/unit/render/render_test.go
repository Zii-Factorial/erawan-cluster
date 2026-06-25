// Package render_test holds black-box unit tests for internal/render.
package render_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"erawan-cluster/internal/render"
)

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (raw=%s)", err, rr.Body.String())
	}
	return body
}

func TestOKEnvelope(t *testing.T) {
	rr := httptest.NewRecorder()
	render.OK(rr, "done", map[string]any{"id": "j1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected JSON content type, got %q", ct)
	}
	body := decodeBody(t, rr)
	if body["status"] != "ok" || body["message"] != "done" {
		t.Fatalf("unexpected envelope: %v", body)
	}
	if _, ok := body["data"]; !ok {
		t.Fatal("expected data field to be present")
	}
}

func TestAcceptedStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	render.Accepted(rr, "started", nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rr.Code)
	}
	if _, ok := decodeBody(t, rr)["data"]; ok {
		t.Fatal("nil data should be omitted")
	}
}

func TestErrorEnvelope(t *testing.T) {
	rr := httptest.NewRecorder()
	render.Error(rr, http.StatusBadRequest, "bad input")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	body := decodeBody(t, rr)
	if body["status"] != "error" || body["message"] != "bad input" {
		t.Fatalf("unexpected error envelope: %v", body)
	}
}

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	// Valid
	var p payload
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x"}`))
	if err := render.DecodeJSON(req, &p); err != nil || p.Name != "x" {
		t.Fatalf("expected valid decode, got %v / %+v", err, p)
	}

	// Empty body
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	if err := render.DecodeJSON(req, &p); err == nil {
		t.Fatal("expected error for empty body")
	}

	// Unknown field rejected
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x","extra":1}`))
	if err := render.DecodeJSON(req, &p); err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
}
