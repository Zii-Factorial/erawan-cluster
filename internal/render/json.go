package render

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Envelope is the standard JSON response wrapper.
type Envelope map[string]any

// JSON writes a JSON response with the given status code.
func JSON(w http.ResponseWriter, status int, payload Envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// OK writes a 200 JSON response.
func OK(w http.ResponseWriter, message string, data any) {
	body := Envelope{"status": "ok", "message": message}
	if data != nil {
		body["data"] = data
	}
	JSON(w, http.StatusOK, body)
}

// Accepted writes a 202 JSON response.
func Accepted(w http.ResponseWriter, message string, data any) {
	body := Envelope{"status": "ok", "message": message}
	if data != nil {
		body["data"] = data
	}
	JSON(w, http.StatusAccepted, body)
}

// Error writes a JSON error response.
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, Envelope{"status": "error", "message": message})
}

// DecodeJSON decodes the request body into dst, disallowing unknown fields.
func DecodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is required")
		}
		return err
	}
	return nil
}
