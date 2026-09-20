//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Niboor/notekeeper/core/internal/testdb"
)

// Least privilege between components (NFR-S2, SEC-OPS-5, SEC-AUD-2): the runtime role cannot change the
// schema or rewrite the audit log, and the bot's role cannot read anything of Core's.
func TestRolesHaveOnlyTheirOwnPrivileges(t *testing.T) {
	ctx := context.Background()
	d := testdb.New(t)
	connect := func(role, pw string) *pgxpool.Pool {
		url := strings.Replace(d.AppURL, "nk_app:nk_app_dev", role+":"+pw, 1)
		pool, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	app, bot := d.App, connect("nk_bot_matrix", "nk_bot_dev")

	mustFail := func(pool *pgxpool.Pool, what, sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Errorf("%s was allowed: %s", what, sql)
		}
	}
	// Core at runtime: data yes, schema and audit history no.
	mustFail(app, "creating a table", `create table intruder (id int)`)
	mustFail(app, "altering the schema", `alter table users add column injected text`)
	mustFail(app, "dropping a table", `drop table audit_log`)
	mustFail(app, "rewriting the audit log", `update audit_log set action = 'nothing happened'`)
	mustFail(app, "deleting from the audit log", `delete from audit_log`)
	mustFail(app, "truncating the audit log", `truncate audit_log`)
	if _, err := app.Exec(ctx, `insert into audit_log (actor_kind, action) values ('system', 'test.entry')`); err != nil {
		t.Fatalf("the runtime role must be able to append to the audit log: %v", err)
	}
	// The bot: its own schema only.
	for _, table := range []string{"users", "notes", "note_parts", "attachments", "blob_chunks", "sessions", "bot_credentials", "external_identities", "audit_log", "share_links"} {
		if _, err := bot.Exec(ctx, `select count(*) from public.`+table); err == nil {
			t.Errorf("the bot role can read %s", table)
		}
	}
	if _, err := bot.Exec(ctx, `create table if not exists own_state (k text primary key)`); err != nil {
		t.Fatalf("the bot must be able to keep its own state: %v", err)
	}
	mustFail(bot, "creating in Core's schema", `create table public.intruder (id int)`)
}
