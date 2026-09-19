package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	"github.com/Niboor/notekeeper/core/internal/config"
	"github.com/Niboor/notekeeper/core/internal/gen/botapi"
	"github.com/Niboor/notekeeper/core/internal/gen/publicapi"
	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
)

func testRouters(t *testing.T, cfg config.Config, ready Readiness) Routers {
	t.Helper()
	r, err := NewRouters(Deps{Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Ready: ready})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func routes(t *testing.T, h http.Handler) []string {
	t.Helper()
	var out []string
	err := chi.Walk(h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out = append(out, method+" "+route)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func specOps(t *testing.T, get func() (*openapi3.T, error)) []string {
	t.Helper()
	spec, err := get()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for path, item := range spec.Paths.Map() {
		for method := range item.Operations() {
			out = append(out, method+" "+path)
		}
	}
	sort.Strings(out)
	return out
}

// TestRoutesMatchSpecs is the CI guard of tech-stack section 3.2 item 5 and SEC-ISO-1: every
// route a listener serves must be an operation in that API's OpenAPI document, and the other
// way round. Hand-written routes (SSE, long-poll) must appear in the spec as stubs.
func TestRoutesMatchSpecs(t *testing.T) {
	r := testRouters(t, config.Config{}, nil)
	for name, tc := range map[string]struct {
		handler http.Handler
		spec    func() (*openapi3.T, error)
	}{
		"user":   {r.User, userapi.GetSpec},
		"bot":    {r.Bot, botapi.GetSpec},
		"public": {r.Public, publicapi.GetSpec},
	} {
		got, want := routes(t, tc.handler), specOps(t, tc.spec)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s API: routes and spec differ\nrouter:\n  %s\nspec:\n  %s", name,
				strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
}

func TestVersionEndpoints(t *testing.T) {
	r := testRouters(t, config.Config{}, nil)
	for path, h := range map[string]http.Handler{
		"/api/v1/version":        r.User,
		"/bot/v1/version":        r.Bot,
		"/api/public/v1/version": r.Public,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		var body map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body["version"] == "" {
			t.Fatalf("%s: bad body %v %v", path, body, err)
		}
	}
}

// Each listener serves only its own routes (docs/design/README section 2).
func TestListenersAreIsolated(t *testing.T) {
	r := testRouters(t, config.Config{}, nil)
	cases := []struct {
		name    string
		h       http.Handler
		path    string
		wantHit bool
	}{
		{"user has no bot route", r.User, "/bot/v1/version", false},
		{"user has no public route", r.User, "/api/public/v1/version", false},
		{"bot has no user route", r.Bot, "/api/v1/version", false},
		{"public has no user route", r.Public, "/api/v1/version", false},
		{"public has no bot route", r.Public, "/bot/v1/version", false},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if (rec.Code == 200) != c.wantHit {
			t.Errorf("%s: status %d", c.name, rec.Code)
		}
		if rec.Code == 404 && rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: 404 is not problem+json", c.name)
		}
	}
}

func TestHostChecksPerListener(t *testing.T) {
	r := testRouters(t, config.Config{AppHosts: []string{"app.example.com"}, ShareHosts: []string{"share.example.net"}}, nil)
	get := func(h http.Handler, host, path string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := get(r.User, "app.example.com", "/api/v1/version"); c != 200 {
		t.Errorf("user on app host: %d", c)
	}
	if c := get(r.User, "share.example.net", "/api/v1/version"); c != 404 {
		t.Errorf("user API reachable on the share host: %d", c)
	}
	if c := get(r.Public, "share.example.net", "/api/public/v1/version"); c != 200 {
		t.Errorf("public on share host: %d", c)
	}
	if c := get(r.Public, "app.example.com", "/api/public/v1/version"); c != 404 {
		t.Errorf("public API reachable on the app host: %d", c)
	}
}

func TestOpsEndpoints(t *testing.T) {
	ready := true
	r := testRouters(t, config.Config{}, func(context.Context) error {
		if !ready {
			return errors.New("db down")
		}
		return nil
	})
	code := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		r.Ops.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
	if c, _ := code("/healthz"); c != 200 {
		t.Errorf("healthz %d", c)
	}
	if c, _ := code("/readyz"); c != 200 {
		t.Errorf("readyz %d", c)
	}
	ready = false
	c, body := code("/readyz")
	if c != 503 || strings.Contains(body, "db down") {
		t.Errorf("not-ready readyz: %d %q (reason must not leak)", c, body)
	}
	// serve one API request so a metric exists, then read /metrics
	r.User.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	if c, body := code("/metrics"); c != 200 || !strings.Contains(body, "nk_http_requests_total") {
		t.Errorf("metrics %d, missing request counter", c)
	}
}

// Every API listener answers with the defensive headers, including for errors (SEC-API-8, SEC-DATA-6).
func TestAPIResponsesCarrySecurityHeaders(t *testing.T) {
	r := testRouters(t, config.Config{}, nil)
	for name, tc := range map[string]struct {
		h    http.Handler
		path string
	}{
		"user ok":        {r.User, "/api/v1/version"},
		"user not found": {r.User, "/api/v1/nothing"},
		"bot ok":         {r.Bot, "/bot/v1/version"},
		"bot not found":  {r.Bot, "/bot/v1/nothing"},
	} {
		rec := httptest.NewRecorder()
		tc.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		for header, want := range map[string]string{
			"Cache-Control":          "no-store",
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "no-referrer",
		} {
			if got := rec.Header().Get(header); got != want {
				t.Errorf("%s: %s = %q, want %q", name, header, got, want)
			}
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: CSP %q", name, csp)
		}
	}
}
