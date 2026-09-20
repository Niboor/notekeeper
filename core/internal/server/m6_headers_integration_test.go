//go:build integration

package server_test

import (
	"net/http"
	"strings"
	"testing"
)

// The API is not for other sites: it grants no cross-origin access, answers preflights without
// permission, refuses state changes from foreign origins, and cannot be framed (SEC-API-6, SEC-API-8, SEC-DATA-3).
func TestNoCrossOriginAccessAndBaselineHeaders(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n := u.note("", "mine", nil)

	do := func(method, path string, headers map[string]string) response {
		req, _ := http.NewRequest(method, s.user.URL+path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		for k, v := range u.c.cookies {
			req.AddCookie(&http.Cookie{Name: k, Value: v})
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		return response{Status: res.StatusCode, Header: res.Header}
	}
	foreign := map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site", "X-Notekeeper-Client": "web"}

	// No CORS grant on any answer: not on reads, not on preflights, not on errors.
	for name, res := range map[string]response{
		"read":      do("GET", "/api/v1/me", foreign),
		"preflight": do("OPTIONS", "/api/v1/notes", map[string]string{"Origin": "https://evil.example", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "x-notekeeper-client"}),
		"unknown":   do("GET", "/api/v1/nothing", foreign),
	} {
		for h := range res.Header {
			if strings.HasPrefix(strings.ToLower(h), "access-control-allow") {
				t.Errorf("%s: %s = %v", name, h, res.Header[h])
			}
		}
	}
	// A page on another site cannot make the browser change anything with the person's cookies.
	if res := do("DELETE", "/api/v1/notes/"+n.ID, foreign); res.Status != 403 {
		t.Fatalf("cross-site delete: %d", res.Status)
	}
	if res := do("POST", "/api/v1/notes/"+n.ID+"/dismiss", map[string]string{"Origin": "https://evil.example", "X-Notekeeper-Client": "web"}); res.Status != 403 {
		t.Fatalf("foreign origin without fetch metadata: %d", res.Status)
	}
	if u.c.do("GET", "/api/v1/notes/"+n.ID, nil).Status != 200 {
		t.Fatal("the note must be untouched")
	}
	// Headers every answer carries.
	h := do("GET", "/api/v1/me", map[string]string{"X-Notekeeper-Client": "web", "Sec-Fetch-Site": "same-origin"}).Header
	if !strings.Contains(h.Get("Strict-Transport-Security"), "max-age=") || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" ||
		!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || strings.Contains(h.Get("Content-Security-Policy"), "unsafe") || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", h)
	}
}
