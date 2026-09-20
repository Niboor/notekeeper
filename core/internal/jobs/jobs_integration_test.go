//go:build integration

package jobs_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/Niboor/notekeeper/core/internal/jobs"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func deps(d *testdb.DB, now time.Time) jobs.Deps {
	return jobs.Deps{Store: store.New(d.App), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
}

// The runtime role can run the queue: River's tables (created by `core migrate` under the schema
// owner) are reachable by nk_app, no row-level security applies to them, and a job enqueued in a
// transaction is worked (docs/design/05 section 3).
func TestQueueWorksAsTheRuntimeRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d := testdb.New(t)
	client, err := jobs.New(d.App, deps(d, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	events, cancelSub := client.Subscribe(river.EventKindJobCompleted)
	defer cancelSub()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("start (needs access to River's tables as nk_app): %v", err)
	}
	defer func() { _ = client.Stop(context.Background()) }()

	tx, err := d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := client.InsertTx(ctx, tx, jobs.PurgeArgs{}, nil); err != nil {
		t.Fatalf("enqueue as nk_app: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case ev := <-events:
			if ev.Job.Kind == "purge" && ev.Job.FinalizedAt != nil {
				goto worked
			}
		case <-ctx.Done():
			t.Fatal("job was not worked")
		}
	}
worked:
	var rls bool
	if err := d.Admin.QueryRow(ctx, `select coalesce(bool_or(relrowsecurity), false) from pg_class where relname like 'river_%' and relkind = 'r'`).Scan(&rls); err != nil || rls {
		t.Fatalf("row-level security must not apply to River's tables: %v %v", rls, err)
	}
}

func TestPurgeAppliesRetention(t *testing.T) {
	ctx := context.Background()
	d := testdb.New(t)
	now := time.Now()
	old, fresh := now.Add(-100*24*time.Hour), now.Add(-time.Hour)
	a, b := uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{a, b} {
		if _, err := d.Admin.Exec(ctx, `insert into users (id, username, display_name, status) values ($1, $2, 'x', 'active')`, u, "u"+u.String()[:8]); err != nil {
			t.Fatal(err)
		}
		for i, at := range []time.Time{old, fresh} {
			if _, err := d.Admin.Exec(ctx, `insert into changes (user_id, seq, entity_type, entity_id, op, created_at) values ($1, $2, 'note', $3, 'upsert', $4)`,
				u, i+1, uuid.New(), at); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Operational tables: an old and a fresh row each.
	for _, q := range []string{
		`insert into auth_throttle (key, failures, updated_at) values ('old', 1, now() - interval '3 hours'), ('fresh', 1, now())`,
		`insert into idempotency_keys (scope, key, request_hash, response, created_at) values ('s','old','\x00','{}', now() - interval '2 days'), ('s','fresh','\x00','{}', now())`,
		`insert into sessions (id, user_id, client_kind, label, expires_at, absolute_expires_at, revoked_at)
		   values (gen_random_uuid(), '` + a.String() + `', 'web', 'old', now(), now(), now() - interval '60 days'),
		          (gen_random_uuid(), '` + a.String() + `', 'web', 'fresh', now() + interval '1 day', now() + interval '1 day', null)`,
	} {
		if _, err := d.Admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	if err := jobs.Purge(ctx, deps(d, now)); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := d.Admin.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`select count(*) from changes`); n != 2 { // one fresh row per user
		t.Errorf("changes left: %d, want 2 (only the recent ones, for both users)", n)
	}
	if n := count(`select count(*) from auth_throttle`); n != 1 {
		t.Errorf("auth_throttle: %d", n)
	}
	if n := count(`select count(*) from idempotency_keys`); n != 1 {
		t.Errorf("idempotency_keys: %d", n)
	}
	if n := count(`select count(*) from sessions`); n != 1 {
		t.Errorf("sessions: %d (an old revoked session must go, an active one must stay)", n)
	}
}
