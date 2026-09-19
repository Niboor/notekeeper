package server

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/httpx"
)

// Cookie names (docs/design/03-auth.md section 2.2). The __Host- prefix makes browsers refuse
// the cookie unless it is Secure, host-only and Path=/; __Secure- requires Secure.
const (
	accessCookie  = "__Host-nka"
	refreshCookie = "__Secure-nkr"
	refreshPath   = "/api/v1/auth"
	clientHeader  = "X-Notekeeper-Client"
)

type principalKey struct{}

// PrincipalFrom returns the authenticated user of a request, if any.
func PrincipalFrom(ctx context.Context) (accounts.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*accounts.Principal)
	if !ok || p == nil {
		return accounts.Principal{}, false
	}
	return *p, true
}

func mustPrincipal(ctx context.Context) (accounts.Principal, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return accounts.Principal{}, errUnauthenticated
	}
	return p, nil
}

// userAuth authenticates requests to the user API and enforces the security declared in the
// OpenAPI document. It runs after routing (as a per-route middleware), so it knows the route.
type userAuth struct {
	accts    *accounts.Service
	reqs     map[string]Requirement
	appHosts []string
	trusted  []netip.Prefix
}

// wrap protects h. Hand-written handlers (SSE) are wrapped explicitly; generated ones get it
// through the router options.
func (a *userAuth) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pattern := ""
		if rc := chi.RouteContext(r.Context()); rc != nil {
			pattern = rc.RoutePattern()
		}
		req, declared := a.reqs[opKey(r.Method, pattern)]
		if !declared {
			// Fail closed: a route the OpenAPI document does not describe is not served.
			httpx.NotFound(w, r)
			return
		}
		bearer := bearerToken(r)
		if unsafeMethod(r.Method) && bearer == "" && !a.sameOriginWeb(r) {
			httpx.WriteError(w, r, httpx.NewError(http.StatusForbidden, "csrf"))
			return
		}
		if req == Anonymous {
			next(h, w, r, nil)
			return
		}
		token := bearer
		if token == "" {
			if c, err := r.Cookie(accessCookie); err == nil {
				token = c.Value
			}
		}
		p, err := a.accts.Authenticate(r.Context(), token)
		if err != nil {
			if mapped := mapError(err); mapped != nil && mapped != errUnauthenticated {
				httpx.WriteError(w, r, mapped)
				return
			}
			httpx.WriteError(w, r, errUnauthenticated)
			return
		}
		if req == Admin && !p.IsAdmin {
			httpx.WriteError(w, r, errForbidden)
			return
		}
		next(h, w, r, p)
	})
}

func next(h http.Handler, w http.ResponseWriter, r *http.Request, p *accounts.Principal) {
	ctx := r.Context()
	if p != nil {
		ctx = context.WithValue(ctx, principalKey{}, p)
	}
	h.ServeHTTP(w, r.WithContext(ctx))
}

func bearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(v, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// sameOriginWeb implements the CSRF rules for state-changing requests that carry cookies
// (docs/design/03-auth.md section 2.4, SEC-AUTH-8): the custom client header, which a
// cross-site form cannot set, and a same-origin fetch-metadata or Origin check.
func (a *userAuth) sameOriginWeb(r *http.Request) bool {
	if r.Header.Get(clientHeader) != "web" {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin":
		return true
	case "":
		// Older browsers and non-browser clients: fall back to the Origin header.
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // not a browser cross-site request
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		host := strings.ToLower(u.Host)
		if len(a.appHosts) > 0 {
			for _, h := range a.appHosts {
				if strings.EqualFold(h, u.Hostname()) || strings.EqualFold(h, host) {
					return true
				}
			}
			return false
		}
		return strings.EqualFold(host, r.Host)
	}
	return false
}
