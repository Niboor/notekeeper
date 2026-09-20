//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/db"
	"github.com/Niboor/notekeeper/core/internal/testdb"
)

// Data migrations run as the schema owner, which is not a superuser, while the content tables have
// row-level security FORCED: a migration that touches them without setting app.user_id changes
// nothing in production, though it would change everything if run as a superuser. This test runs
// migration 0003 (which backfills the search index) over existing data under the real role and
// proves the backfill reaches every user's notes (docs/design/01-data-model.md section 12).
func TestBackfillMigrationReachesEveryUsersRows(t *testing.T) {
	ctx := context.Background()
	d := testdb.NewUnmigrated(t)
	if err := db.MigrateTo(ctx, d.MigrateURL, 2); err != nil {
		t.Fatal(err)
	}
	var want int
	for _, name := range []string{"alice", "bob"} {
		user := uuid.New()
		if _, err := d.Admin.Exec(ctx, `insert into users (id, username, display_name, status) values ($1, $2, $2, 'active')`, user, name); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			note := uuid.New()
			if _, err := d.Admin.Exec(ctx, `insert into notes (id, user_id, created_at) values ($1, $2, now())`, note, user); err != nil {
				t.Fatal(err)
			}
			if _, err := d.Admin.Exec(ctx, `insert into note_parts (id, user_id, note_id, ordinal, kind, text, attach_reason, created_at)
				values ($1, $2, $3, 0, 'text', 'backfill me', 'first', now())`, uuid.New(), user, note); err != nil {
				t.Fatal(err)
			}
			want++
		}
	}
	if err := db.Migrate(ctx, d.MigrateURL); err != nil {
		t.Fatalf("migration 0003 under the non-superuser owner: %v", err)
	}
	var got int
	if err := d.Admin.QueryRow(ctx, `select count(*) from note_search where body = 'backfill me'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("backfill indexed %d of %d notes: row-level security hid the rest from the migration role", got, want)
	}
	// The runtime role can use what the migration created.
	var who string
	if err := d.App.QueryRow(ctx, `select current_user`).Scan(&who); err != nil || who != "nk_app" {
		t.Fatal(who, err)
	}
}

// Every connection carries a statement timeout and an idle-in-transaction timeout, so one stuck query
// cannot hold the shared pool (SEC-API-4).
func TestPoolSetsTimeouts(t *testing.T) {
	d := testdb.New(t)
	pool, err := db.Open(context.Background(), d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var stmt, idle string
	if err := pool.QueryRow(context.Background(), `select current_setting('statement_timeout'), current_setting('idle_in_transaction_session_timeout')`).Scan(&stmt, &idle); err != nil {
		t.Fatal(err)
	}
	if stmt != "30s" || idle != "1min" {
		t.Fatalf("timeouts: statement %s, idle %s", stmt, idle)
	}
}
