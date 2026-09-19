package httpx

import (
	"context"
	"net/http"
)

type httpCtxKey struct{}

type httpPair struct {
	W http.ResponseWriter
	R *http.Request
}

// WithHTTP stores the raw response writer and request in ctx. Strict-server handlers receive
// only a context; the few that must set cookies or read a header (login, refresh, range
// downloads) fetch them from here.
func WithHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
	return context.WithValue(ctx, httpCtxKey{}, httpPair{W: w, R: r})
}

// HTTPFrom returns the stored response writer and request.
func HTTPFrom(ctx context.Context) (http.ResponseWriter, *http.Request) {
	p, _ := ctx.Value(httpCtxKey{}).(httpPair)
	return p.W, p.R
}
