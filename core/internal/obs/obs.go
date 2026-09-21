// Package obs holds the application's business metrics (requirement NFR-O2, SEC-AUD-3): what the
// service did, as counters that dashboards and alerts read, next to the HTTP and runtime metrics that
// come with the listeners. Labels are small closed sets, never identifiers, so a metric can neither
// grow without bound nor carry note content or personal data (SEC-DATA-1).
package obs

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Niboor/notekeeper/core/internal/version"
)

// Registry is served on the ops listener together with the request metrics.
var Registry = prometheus.NewRegistry()

func counter(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	Registry.MustRegister(c)
	return c
}

// Counters.
var (
	Ingest         = counter("nk_ingest_events_total", "Chat events handled, by kind and result.", "kind", "result")
	Grouping       = counter("nk_grouping_decisions_total", "Where a new chat message went, by reason (GRP-8).", "reason")
	AuthEvents     = counter("nk_auth_events_total", "Sign-in outcomes: succeeded, failed, throttled, and refresh_reuse (an old refresh token was presented again and its session revoked, SEC-AUTH-7).", "event")
	BotRejected    = counter("nk_bot_requests_rejected_total", "Bot API requests refused before they did anything, by reason.", "reason")
	ShareRequests  = counter("nk_share_requests_total", "Public share requests by result: ok, not_found, limited.", "result")
	RemindersFired = counter("nk_reminders_fired_total", "Reminders fired, by whether they were late.", "late")
	OutboxResults  = counter("nk_outbox_results_total", "What bots reported about outbox items, by kind and result.", "kind", "result")
	JobFailures    = counter("nk_job_failures_total", "Background jobs that returned an error, by task.", "task")
	JobRuns        = counter("nk_job_runs_total", "Background job runs, by task and result (ok, error): with the failures, the share of runs that fail.", "task", "result")
)

// StreamsRefused counts realtime streams turned away because the user already has the most allowed
// (SEC-API-4): what a browser that never closes its old streams looks like.
var StreamsRefused = func() prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: "nk_realtime_streams_refused_total", Help: "Realtime streams refused because the user had too many open."})
	Registry.MustRegister(c)
	return c
}()

// The label is `task` and not `job`: Prometheus adds a `job` label of its own to everything it scrapes and
// would rename a metric's own to `exported_job`.

// JobDuration and JobLastSuccess say how long a job takes and when it last worked: the second is what
// an alert reads, because a job that stopped running produces no failures at all.
var (
	JobDuration = func() *prometheus.HistogramVec {
		h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "nk_job_duration_seconds", Help: "How long a background job run takes, by task.",
			Buckets: []float64{.01, .05, .1, .5, 1, 5, 15, 60, 300}}, []string{"task"})
		Registry.MustRegister(h)
		return h
	}()
	JobLastSuccess = func() *prometheus.GaugeVec {
		g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nk_job_last_success_timestamp_seconds", Help: "Unix time of the last successful run of a background job, by task."}, []string{"task"})
		Registry.MustRegister(g)
		return g
	}()
)

func init() {
	// The version that is running, as a label, so a dashboard can show it and a rollout can be seen.
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nk_build_info", Help: "Always 1; the version of this build is the label."}, []string{"component", "version"})
	g.WithLabelValues("core", version.Version).Set(1)
	Registry.MustRegister(g)
}

// ReminderLag is how long after their due time reminders fired: the scheduling lag (CORE-R5).
var ReminderLag = func() prometheus.Histogram {
	h := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "nk_reminder_lag_seconds", Help: "Seconds between a reminder's due time and its firing.",
		Buckets: []float64{1, 5, 10, 20, 30, 60, 120, 300, 900, 3600}})
	Registry.MustRegister(h)
	return h
}()
