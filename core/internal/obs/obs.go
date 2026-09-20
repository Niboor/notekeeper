// Package obs holds the application's business metrics (requirement NFR-O2, SEC-AUD-3): what the
// service did, as counters that dashboards and alerts read, next to the HTTP and runtime metrics that
// come with the listeners. Labels are small closed sets, never identifiers, so a metric can neither
// grow without bound nor carry note content or personal data (SEC-DATA-1).
package obs

import "github.com/prometheus/client_golang/prometheus"

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
	AuthEvents     = counter("nk_auth_events_total", "Sign-in outcomes: succeeded, failed, throttled.", "event")
	BotRejected    = counter("nk_bot_requests_rejected_total", "Bot API requests refused before they did anything, by reason.", "reason")
	ShareRequests  = counter("nk_share_requests_total", "Public share requests by result: ok, not_found, limited.", "result")
	RemindersFired = counter("nk_reminders_fired_total", "Reminders fired, by whether they were late.", "late")
	OutboxResults  = counter("nk_outbox_results_total", "What bots reported about outbox items, by kind and result.", "kind", "result")
	JobFailures    = counter("nk_job_failures_total", "Background jobs that returned an error, by job.", "job")
)

// ReminderLag is how long after their due time reminders fired: the scheduling lag (CORE-R5).
var ReminderLag = func() prometheus.Histogram {
	h := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "nk_reminder_lag_seconds", Help: "Seconds between a reminder's due time and its firing.",
		Buckets: []float64{1, 5, 10, 20, 30, 60, 120, 300, 900, 3600}})
	Registry.MustRegister(h)
	return h
}()
