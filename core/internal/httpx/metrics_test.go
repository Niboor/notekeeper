package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// NFR-O2: request latency is measured for requests, not for realtime streams, which stay open for hours by design.
func TestStreamsAreNotTimedAsRequests(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	serve := func(contentType string) {
		h := Observe("user", log, m)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			w.WriteHeader(http.StatusOK)
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}
	serve("application/json")
	serve("text/event-stream")
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var requests, timed float64
	for _, f := range families {
		switch f.GetName() {
		case "nk_http_requests_total":
			for _, mm := range f.GetMetric() {
				requests += mm.GetCounter().GetValue()
			}
		case "nk_http_request_duration_seconds":
			for _, mm := range f.GetMetric() {
				timed += float64(mm.GetHistogram().GetSampleCount())
			}
		}
	}
	if requests != 2 || timed != 1 {
		t.Fatalf("requests counted %v (want 2), timed %v (want 1)", requests, timed)
	}
}
