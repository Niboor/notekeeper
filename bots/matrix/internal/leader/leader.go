// Package leader makes sure only one bot instance syncs at a time, using a PostgreSQL
// session-level advisory lock on a dedicated connection (docs/design/06-matrix-bot.md section 2,
// MX-N2, SEC-MX-4). Two instances syncing the same account would each ack events the other
// never processed, so a process that might have lost the lock must stop at once.
package leader

import (
	"context"
	"errors"
	"hash/fnv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Lock is a held advisory lock. It is released when Close is called or the connection drops.
type Lock struct {
	conn *pgx.Conn
	lost chan struct{}
	once sync.Once
	stop context.CancelFunc
}

// key derives the advisory lock number from the instance name.
func key(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("matrix-bot:" + name))
	return int64(h.Sum64())
}

// TryAcquire takes the lock for name if nobody else holds it. It returns (nil, nil) when another
// instance holds it.
func TryAcquire(ctx context.Context, databaseURL, name string) (*Lock, error) {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	var got bool
	if err := conn.QueryRow(ctx, `select pg_try_advisory_lock($1)`, key(name)).Scan(&got); err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	if !got {
		_ = conn.Close(ctx)
		return nil, nil
	}
	monitorCtx, stop := context.WithCancel(context.Background())
	l := &Lock{conn: conn, lost: make(chan struct{}), stop: stop}
	go l.monitor(monitorCtx)
	return l, nil
}

// Acquire waits until the lock is free, retrying every interval, or ctx ends.
func Acquire(ctx context.Context, databaseURL, name string, interval time.Duration, onWait func()) (*Lock, error) {
	for {
		l, err := TryAcquire(ctx, databaseURL, name)
		if err != nil {
			return nil, err
		}
		if l != nil {
			return l, nil
		}
		if onWait != nil {
			onWait()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// monitor pings the connection; a failure means the session (and so the lock) is gone.
func (l *Lock) monitor(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			var one int
			err := l.conn.QueryRow(pingCtx, `select 1`).Scan(&one)
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				l.once.Do(func() { close(l.lost) })
				return
			}
		}
	}
}

// Lost is closed when the lock connection failed. The process must exit: it can no longer
// prove that it is the only instance.
func (l *Lock) Lost() <-chan struct{} { return l.lost }

// Close releases the lock.
func (l *Lock) Close() {
	l.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = l.conn.Close(ctx)
}
