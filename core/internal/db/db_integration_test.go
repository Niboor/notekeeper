//go:build integration

package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// pgImages returns the PostgreSQL images to test against: 16 (the supported floor, NFR-D8)
// and the newest release. NK_TEST_PG_IMAGES overrides it (comma separated).
func pgImages() []string {
	if v := os.Getenv("NK_TEST_PG_IMAGES"); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"postgres:16", "postgres:latest"}
}

func startPostgres(t *testing.T, image string) string {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, image,
		postgres.WithDatabase("notekeeper"), postgres.WithUsername("nk"), postgres.WithPassword("nk"),
		postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start %s: %v", image, err)
	}
	testcontainers.CleanupContainer(t, c)
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return url
}

// Also demonstrates: NFR-D4. It runs on PostgreSQL 16, the supported floor, and on the newest release (NFR-D8).
func TestMigrateAndReadiness(t *testing.T) {
	for _, image := range pgImages() {
		t.Run(image, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			url := startPostgres(t, image)

			pool, err := Open(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()

			// Before migrating, a replica must not report ready.
			if ok, err := MigrationsCurrent(ctx, pool); err == nil && ok {
				t.Fatal("fresh database reported as migrated")
			}

			if err := Migrate(ctx, url); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			if err := Migrate(ctx, url); err != nil {
				t.Fatalf("second migrate must be a no-op: %v", err)
			}
			if ok, err := MigrationsCurrent(ctx, pool); err != nil || !ok {
				t.Fatalf("after migrate: current=%v err=%v", ok, err)
			}

			// The baseline provides the immutable unaccent wrapper used by search.
			var got string
			if err := pool.QueryRow(ctx, `select nk_unaccent('Café Ünïcode')`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != "Cafe Unicode" {
				t.Fatalf("nk_unaccent = %q", got)
			}
			var immutable string
			if err := pool.QueryRow(ctx, `select provolatile::text from pg_proc where proname = 'nk_unaccent'`).Scan(&immutable); err != nil {
				t.Fatal(err)
			}
			if immutable != "i" {
				t.Fatalf("nk_unaccent volatility = %q, want immutable", immutable)
			}
			// pg_trgm is available for prefix and typo-tolerant search.
			var sim float64
			if err := pool.QueryRow(ctx, `select similarity('ticket','tickets')`).Scan(&sim); err != nil || sim <= 0 {
				t.Fatalf("pg_trgm: %v %v", sim, err)
			}
		})
	}
}
