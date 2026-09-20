package httpx

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func ok() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
}

func TestHostCheck(t *testing.T) {
	h := HostCheck([]string{"app.example.com"})(ok())
	for host, want := range map[string]int{
		"app.example.com":      http.StatusNoContent,
		"APP.example.com:8080": http.StatusNoContent,
		"share.example.net":    http.StatusNotFound,
		"":                     http.StatusNotFound,
	} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("host %q: got %d, want %d", host, rec.Code, want)
		}
	}
}

func TestHostCheckEmptyAllowsAll(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Host = "anything"
	HostCheck(nil)(ok()).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestProblemShape(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	RequestID(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { NotFound(w, r) })).ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type %q", ct)
	}
	var p Problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Status != 404 || p.Code != "not_found" || p.RequestID == "" || p.Title != "Not Found" {
		t.Fatalf("unexpected problem %+v", p)
	}
	if rec.Header().Get("X-Request-ID") != p.RequestID {
		t.Fatal("request id header and body differ")
	}
}

func TestRequestIDTrust(t *testing.T) {
	get := func(trust bool, in string) string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Request-ID", in)
		RequestID(trust)(ok()).ServeHTTP(rec, req)
		return rec.Header().Get("X-Request-ID")
	}
	if got := get(true, "bot-123"); got != "bot-123" {
		t.Errorf("trusted id not kept: %q", got)
	}
	if got := get(false, "bot-123"); got == "bot-123" {
		t.Error("untrusted id was accepted")
	}
	if got := get(true, "bad id\r\n"); got == "bad id\r\n" || got == "" {
		t.Errorf("unsafe id handling: %q", got)
	}
}

// Also demonstrates: SEC-API-5.
func TestRecovererHidesPanic(t *testing.T) {
	h := RequestID(false)(Recoverer(slog.New(slog.NewTextHandler(io.Discard, nil)))(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom secret") })))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
	if b := rec.Body.String(); len(b) == 0 || contains(b, "boom") {
		t.Fatalf("panic details leaked or empty body: %q", b)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
