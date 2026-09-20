package obs

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OutboxDepth reports how many outbox items are in each state, per bot instance kind of state only: the
// number that tells an operator a bot is behind or gone (NFR-O2, "bot lag").
type OutboxDepth func(ctx context.Context) (map[string]int64, error)

// StateCollector reports values that are read when Prometheus scrapes: connection pool use, open
// realtime streams and the outbox backlog. It queries nothing heavier than a grouped count.
type StateCollector struct {
	Pool    *pgxpool.Pool
	Streams func() int
	Outbox  OutboxDepth

	poolConns, poolIdle, poolMax, streams *prometheus.Desc
	outbox                                *prometheus.Desc
}

// NewStateCollector creates the collector.
func NewStateCollector(pool *pgxpool.Pool, streams func() int, outbox OutboxDepth) *StateCollector {
	d := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(name, help, labels, nil)
	}
	return &StateCollector{Pool: pool, Streams: streams, Outbox: outbox,
		poolConns: d("nk_db_pool_connections", "Open database connections."),
		poolIdle:  d("nk_db_pool_idle_connections", "Idle database connections."),
		poolMax:   d("nk_db_pool_max_connections", "Configured pool size."),
		streams:   d("nk_realtime_streams", "Open server-sent event streams."),
		outbox:    d("nk_outbox_items", "Outbox items by state.", "state"),
	}
}

// Describe implements prometheus.Collector.
func (c *StateCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.poolConns, c.poolIdle, c.poolMax, c.streams, c.outbox} {
		ch <- d
	}
}

// Collect implements prometheus.Collector.
func (c *StateCollector) Collect(ch chan<- prometheus.Metric) {
	if c.Pool != nil {
		st := c.Pool.Stat()
		ch <- prometheus.MustNewConstMetric(c.poolConns, prometheus.GaugeValue, float64(st.TotalConns()))
		ch <- prometheus.MustNewConstMetric(c.poolIdle, prometheus.GaugeValue, float64(st.IdleConns()))
		ch <- prometheus.MustNewConstMetric(c.poolMax, prometheus.GaugeValue, float64(st.MaxConns()))
	}
	if c.Streams != nil {
		ch <- prometheus.MustNewConstMetric(c.streams, prometheus.GaugeValue, float64(c.Streams()))
	}
	if c.Outbox != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if depth, err := c.Outbox(ctx); err == nil {
			for state, n := range depth {
				ch <- prometheus.MustNewConstMetric(c.outbox, prometheus.GaugeValue, float64(n), state)
			}
		}
	}
}
