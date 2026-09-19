package server

import (
	"errors"
	"log/slog"
	"math"
	"net/http"

	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"github.com/Niboor/notekeeper/core/internal/ingest"
	"github.com/Niboor/notekeeper/core/internal/notes"
	"github.com/Niboor/notekeeper/core/internal/store"
)

// Standard errors. Titles are generic, codes are stable, nothing leaks internals (SEC-API-5).
var (
	errUnauthenticated = httpx.NewError(http.StatusUnauthorized, "unauthenticated")
	errForbidden       = httpx.NewError(http.StatusForbidden, "forbidden")
	errNotFound        = httpx.NewError(http.StatusNotFound, "not_found")
	errBadRequest      = httpx.NewError(http.StatusBadRequest, "bad_request")
)

// mapError turns service errors into problem responses.
func mapError(err error) *httpx.Error {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he
	}
	var th *accounts.ThrottledError
	switch {
	case errors.As(err, &th):
		secs := int(math.Ceil(th.RetryAfter.Seconds()))
		return &httpx.Error{Status: http.StatusTooManyRequests, Code: "throttled", RetryAfter: max(secs, 1)}
	case errors.Is(err, accounts.ErrInvalidCredentials):
		return httpx.NewError(http.StatusUnauthorized, "invalid_credentials")
	case errors.Is(err, accounts.ErrUnauthenticated), errors.Is(err, bots.ErrUnauthenticated):
		return errUnauthenticated
	case errors.Is(err, accounts.ErrInvalidToken):
		return httpx.NewError(http.StatusBadRequest, "invalid_link")
	case errors.Is(err, accounts.ErrInvalidInput), errors.Is(err, bots.ErrInvalidInput):
		return httpx.NewError(http.StatusBadRequest, "invalid_input").WithDetail(err.Error())
	case errors.Is(err, accounts.ErrConflict), errors.Is(err, bots.ErrConflict):
		return httpx.NewError(http.StatusConflict, "conflict")
	case errors.Is(err, store.ErrNotFound):
		return errNotFound
	case errors.Is(err, notes.ErrInvalidCursor):
		return httpx.NewError(http.StatusBadRequest, "invalid_cursor")
	case errors.Is(err, ingest.ErrInvalid):
		return httpx.NewError(http.StatusBadRequest, "invalid_event").WithDetail(err.Error())
	}
	return nil
}

// errorHandler renders errors returned by strict handlers. Unknown errors are logged with the
// request id and answered with a generic 500.
func errorHandler(log *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		if he := mapError(err); he != nil {
			httpx.WriteError(w, r, he)
			return
		}
		log.Error("request failed", "request_id", httpx.RequestIDFrom(r.Context()), "error", err)
		httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "")
	}
}

func badRequest(w http.ResponseWriter, r *http.Request, _ error) {
	httpx.WriteError(w, r, errBadRequest)
}
