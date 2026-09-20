package bot

import "github.com/prometheus/client_golang/prometheus"

type metrics struct {
	events          *prometheus.CounterVec
	decryptFailures prometheus.Counter
	attachments     *prometheus.CounterVec
	outbox          *prometheus.CounterVec
	gaps            *prometheus.CounterVec
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
		Registry: prometheus.NewRegistry(),
	}
	m.Registry.MustRegister(m.events, m.decryptFailures, m.attachments, m.outbox, m.gaps)
	return m
}

// Registry returns the Prometheus registry served on the bot's metrics endpoint.
func (b *Bot) Registry() *prometheus.Registry { return b.met.Registry }
