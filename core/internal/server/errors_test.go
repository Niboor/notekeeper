package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// NFR-O2: a request the client gave up on is not a server failure. It is not logged as an error and does not
// answer 500, so the 5xx metric and its alert are about real failures; any other unexpected error still does.
func TestClientGoneIsNotAServerError(t *testing.T) {
	var logs bytes.Buffer
	handle := errorHandler(slog.New(slog.NewTextHandler(&logs, nil)))

	gone, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	handle(w, httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(gone), context.Canceled)
	if w.Code != StatusClientClosedRequest || logs.Len() != 0 {
		t.Fatalf("client gone: status %d, log %q; want %d and nothing logged", w.Code, logs.String(), StatusClientClosedRequest)
	}

	// A canceled error while the client is still there is a bug in the server, not a disconnect.
	w = httptest.NewRecorder()
	handle(w, httptest.NewRequest(http.MethodGet, "/x", nil), context.Canceled)
	if w.Code != http.StatusInternalServerError || logs.Len() == 0 {
		t.Fatalf("client present: status %d, log %q; want 500 and a log line", w.Code, logs.String())
	}

	w = httptest.NewRecorder()
	handle(w, httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(gone), errors.New("database is down"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("another error: status %d, want 500", w.Code)
	}
}
