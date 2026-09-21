package bot

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

type metrics struct {
	events          *prometheus.CounterVec
	decryptFailures prometheus.Counter
	attachments     *prometheus.CounterVec
	outbox          *prometheus.CounterVec
	gaps            *prometheus.CounterVec
	coreRetries     *prometheus.CounterVec
	claimFailures   prometheus.Counter
	eventAge        prometheus.Histogram
	leader          prometheus.Gauge
	Registry        *prometheus.Registry
}

func newMetrics() *metrics {
	m := &metrics{
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nk_bot_events_total", Help: "Chat events handled by result (created, ignored, rejected, command, refused, ...).",
		}, []string{"result"}),
		decryptFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "nk_bot_decrypt_failures_total", Help: "Events the bot could not decrypt.",
		}),
		attachments: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nk_bot_attachments_total", Help: "Files moved from chat to Core by result (ok or the failure reason).",
		}, []string{"result"}),
		outbox: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nk_bot_outbox_total", Help: "Messages from Core sent to chats by kind and result.",
		}, []string{"kind", "result"}),
		gaps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nk_bot_sync_gaps_total", Help: "Gaps in a room's timeline that the homeserver left out, by outcome (filled, truncated).",
		}, []string{"outcome"}),
		coreRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nk_bot_core_retries_total", Help: "Calls from the bot to Core that failed and were tried again, by operation: Core is slow, restarting or unreachable.",
		}, []string{"operation"}),
		claimFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "nk_bot_outbox_claim_failures_total", Help: "Times the bot could not ask Core for outbox items, so reminders and notices are not being delivered.",
		}),
		eventAge: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "nk_bot_event_age_seconds", Help: "How old a chat message was when the bot handled it: the delay between sending it and the bot picking it up (after downtime this shows the catch-up).",
			Buckets: []float64{1, 2, 5, 15, 60, 300, 900, 3600, 21600},
		}),
		leader: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "nk_bot_leader", Help: "1 in the replica that holds the single-instance lock and syncs the account, 0 in a standby.",
		}),
		Registry: prometheus.NewRegistry(),
	}
	m.Registry.MustRegister(m.events, m.decryptFailures, m.attachments, m.outbox, m.gaps, m.coreRetries, m.claimFailures, m.eventAge, m.leader,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// RegisterBuildInfo exposes the running version as a label, so a dashboard can show it and a rollout can be seen.
func (b *Bot) RegisterBuildInfo(version string) {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nk_build_info", Help: "Always 1; the version of this build is the label."}, []string{"component", "version"})
	g.WithLabelValues("matrix-bot", version).Set(1)
	b.met.Registry.MustRegister(g)
}

// SetLeader records whether this replica holds the single-instance lock.
func (b *Bot) SetLeader(leading bool) {
	if leading {
		b.met.leader.Set(1)
	} else {
		b.met.leader.Set(0)
	}
}

// CoreRetried counts one retried call to Core; use it as the SDK client's OnRetry.
func (b *Bot) CoreRetried(operation string) { b.met.coreRetries.WithLabelValues(operation).Inc() }

// observeAge records how long ago a chat event was sent.
func (m *metrics) observeAge(sent time.Time) {
	m.eventAge.Observe(max(0, time.Since(sent).Seconds()))
}

// Registry returns the Prometheus registry served on the bot's metrics endpoint.
func (b *Bot) Registry() *prometheus.Registry { return b.met.Registry }
