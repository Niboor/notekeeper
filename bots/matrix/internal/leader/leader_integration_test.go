//go:build integration

package leader

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func startPG(t *testing.T) string {
	t.Helper()
	c, err := postgres.Run(context.Background(), "postgres:16",
		postgres.WithDatabase("nk"), postgres.WithUsername("nk"), postgres.WithPassword("nk"), postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, c)
	url, err := c.ConnectionString(context.Background(), "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return url
}

// Two instances must never both hold the lock (MX-N2, SEC-MX-4); the second takes over when the first goes away.
func TestOnlyOneInstanceLeadsAtATime(t *testing.T) {
	ctx := context.Background()
	url := startPG(t)

	first, err := TryAcquire(ctx, url, "matrix")
	if err != nil || first == nil {
		t.Fatalf("first: %v %v", first, err)
	}
	second, err := TryAcquire(ctx, url, "matrix")
	if err != nil || second != nil {
		t.Fatalf("a second instance took the lock: %v %v", second, err)
	}
	// A different bot instance name has its own lock.
	other, err := TryAcquire(ctx, url, "matrix-2")
	if err != nil || other == nil {
		t.Fatalf("independent instance: %v %v", other, err)
	}
	other.Close()

	// Acquire waits, and gets the lock as soon as the holder is gone.
	got := make(chan *Lock, 1)
	waited := make(chan struct{}, 8)
	go func() {
		l, err := Acquire(ctx, url, "matrix", 100*time.Millisecond, func() {
			select {
			case waited <- struct{}{}:
			default:
			}
		})
		if err != nil {
			t.Error(err)
		}
		got <- l
	}()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the second instance should be waiting")
	}
	first.Close()
	select {
	case l := <-got:
		defer l.Close()
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting instance never took over")
	}
}

// If the lock connection dies, the holder learns it (and the process exits, MX-N2).
func TestLossOfTheLockConnectionIsReported(t *testing.T) {
	ctx := context.Background()
	url := startPG(t)
	l, err := TryAcquire(ctx, url, "matrix")
	if err != nil || l == nil {
		t.Fatal(err)
	}
	defer l.Close()
	// Terminate the session from another connection, as a database failover would.
	admin, err := TryAcquire(ctx, url, "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.conn.Exec(ctx, `select pg_terminate_backend(pid) from pg_locks where locktype = 'advisory' and pid <> pg_backend_pid() and granted`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.Lost():
	case <-time.After(15 * time.Second):
		t.Fatal("loss of the lock connection was not noticed")
	}
}
