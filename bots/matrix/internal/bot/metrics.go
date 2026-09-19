package bot

import "github.com/prometheus/client_golang/prometheus"

type metrics struct {
	events          *prometheus.CounterVec
	decryptFailures prometheus.Counter
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
		Registry: prometheus.NewRegistry(),
	}
	m.Registry.MustRegister(m.events, m.decryptFailures)
	return m
}

// Registry returns the Prometheus registry served on the bot's metrics endpoint.
func (b *Bot) Registry() *prometheus.Registry { return b.met.Registry }
