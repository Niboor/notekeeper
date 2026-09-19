package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

type ctxKey int

const requestIDKey ctxKey = 1

// RequestIDFrom returns the request id stored in ctx, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// RequestID assigns a request id (or accepts one from a trusted caller such as a bot,
// NFR-O1) and echoes it in the X-Request-ID response header.
func RequestID(trustIncoming bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := ""
			if trustIncoming {
				id = sanitizeID(r.Header.Get("X-Request-ID"))
			}
			if id == "" {
				u, err := uuid.NewV7()
				if err != nil {
					u = uuid.New()
				}
				id = u.String()
			}
			w.Header().Set("X-Request-ID", id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
		})
	}
}

func sanitizeID(v string) string {
	if len(v) == 0 || len(v) > 64 {
		return ""
	}
	for _, c := range v {
		isAllowed := c == '-' || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !isAllowed {
			return ""
		}
	}
	return v
}

// Recoverer turns panics into a generic 500 and logs the stack (never sent to the client).
func Recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(rec)
					}
					log.Error("panic in handler", "request_id", RequestIDFrom(r.Context()), "panic", rec, "stack", string(debug.Stack()))
					WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// HostCheck rejects requests whose Host is not one of allowed. With no allowed hosts every
// host is accepted (local development). It is what makes a route unreachable through the
// wrong listener even if ingress is misconfigured (docs/design/README section 2).
func HostCheck(allowed []string) func(http.Handler) http.Handler {
	set := make(map[string]struct{}, len(allowed))
	for _, h := range allowed {
		set[strings.ToLower(h)] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		if len(set) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := strings.ToLower(r.Host)
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if _, ok := set[host]; !ok {
				NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures the response status for logging and metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush keeps streaming responses (SSE, downloads) working through the recorder.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Metrics for HTTP traffic, by listener, route pattern and status class (NFR-O2).
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics registers the HTTP metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nk_http_requests_total", Help: "HTTP requests by listener, route and status.",
		}, []string{"listener", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "nk_http_request_duration_seconds", Help: "HTTP request duration by listener and route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"listener", "route"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

// Observe logs and measures each request. It logs metadata only: never bodies, tokens,
// query strings, filenames or note content (SEC-DATA-1).
func Observe(listener string, log *slog.Logger, m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			route := "unmatched"
			if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
				route = rc.RoutePattern()
			}
			elapsed := time.Since(start)
			if m != nil {
				m.requests.WithLabelValues(listener, route, statusClass(rec.status)).Inc()
				m.duration.WithLabelValues(listener, route).Observe(elapsed.Seconds())
			}
			log.Info("request",
				"listener", listener, "method", r.Method, "route", route, "status", rec.status,
				"duration_ms", elapsed.Milliseconds(), "request_id", RequestIDFrom(r.Context()))
		})
	}
}

func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}
