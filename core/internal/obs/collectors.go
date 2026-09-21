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

// OutboxAge reports how many seconds the oldest outbox item that still waits for a bot has waited.
type OutboxAge func(ctx context.Context) (float64, error)

// StateCollector reports values that are read when Prometheus scrapes: connection pool use, open
// realtime streams and the outbox backlog. It queries nothing heavier than a grouped count.
type StateCollector struct {
	Pool    *pgxpool.Pool
	Streams func() int
	Outbox  OutboxDepth
	Oldest  OutboxAge

	poolConns, poolIdle, poolMax, streams *prometheus.Desc
	outbox, outboxOldest                  *prometheus.Desc
	acquires, acquireWait, acquireBlocked *prometheus.Desc
	acquireCanceled                       *prometheus.Desc
}

// NewStateCollector creates the collector.
func NewStateCollector(pool *pgxpool.Pool, streams func() int, outbox OutboxDepth, oldest OutboxAge) *StateCollector {
	d := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(name, help, labels, nil)
	}
	return &StateCollector{Pool: pool, Streams: streams, Outbox: outbox, Oldest: oldest,
		poolConns:       d("nk_db_pool_connections", "Open database connections."),
		poolIdle:        d("nk_db_pool_idle_connections", "Idle database connections."),
		poolMax:         d("nk_db_pool_max_connections", "Configured pool size."),
		streams:         d("nk_realtime_streams", "Open server-sent event streams."),
		outbox:          d("nk_outbox_items", "Outbox items by state.", "state"),
		outboxOldest:    d("nk_outbox_oldest_wait_seconds", "How long the oldest outbox item that still waits for a bot has waited; 0 when none waits."),
		acquires:        d("nk_db_pool_acquires_total", "Connections taken from the database pool."),
		acquireWait:     d("nk_db_pool_acquire_wait_seconds_total", "Seconds spent waiting for a connection from the database pool, added up."),
		acquireBlocked:  d("nk_db_pool_blocked_acquires_total", "Times a connection was needed and none was idle, so the request waited."),
		acquireCanceled: d("nk_db_pool_canceled_acquires_total", "Times a request gave up waiting for a database connection."),
	}
}

// Describe implements prometheus.Collector.
func (c *StateCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.poolConns, c.poolIdle, c.poolMax, c.streams, c.outbox, c.outboxOldest,
		c.acquires, c.acquireWait, c.acquireBlocked, c.acquireCanceled} {
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
		ch <- prometheus.MustNewConstMetric(c.acquires, prometheus.CounterValue, float64(st.AcquireCount()))
		ch <- prometheus.MustNewConstMetric(c.acquireWait, prometheus.CounterValue, st.AcquireDuration().Seconds())
		ch <- prometheus.MustNewConstMetric(c.acquireBlocked, prometheus.CounterValue, float64(st.EmptyAcquireCount()))
		ch <- prometheus.MustNewConstMetric(c.acquireCanceled, prometheus.CounterValue, float64(st.CanceledAcquireCount()))
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
	if c.Oldest != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if seconds, err := c.Oldest(ctx); err == nil {
			ch <- prometheus.MustNewConstMetric(c.outboxOldest, prometheus.GaugeValue, seconds)
		}
	}
}
