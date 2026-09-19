// Package httpx holds the HTTP plumbing shared by Core's listeners: problem+json errors,
// request ids, logging, host checks, metrics and graceful serving (docs/design/README section 3).
package httpx

import (
	"encoding/json"
	"net/http"
)

// Problem is an RFC 9457 problem document. Details never contain internals (SEC-API-5).
type Problem struct {
	Type      string `json:"type,omitempty"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code,omitempty"`
	Detail    string `json:"detail,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteProblem writes a problem+json response with a generic title for the status.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	p := Problem{
		Type:      "about:blank",
		Title:     http.StatusText(status),
		Status:    status,
		Code:      code,
		Detail:    detail,
		RequestID: RequestIDFrom(r.Context()),
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// NotFound is the single 404 response used for unknown and foreign resources alike (SEC-ISO-3).
func NotFound(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, r, http.StatusNotFound, "not_found", "")
}
