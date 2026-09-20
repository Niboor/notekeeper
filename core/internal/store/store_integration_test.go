//go:build integration

package store_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func newUser(t *testing.T, d *testdb.DB, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := d.Admin.Exec(context.Background(),
		`insert into users (id, username, display_name, status) values ($1, $2, $2, 'active')`, id, name)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func adminNote(t *testing.T, d *testdb.DB, user uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := d.Admin.Exec(context.Background(),
		`insert into notes (id, user_id, created_at) values ($1, $2, now())`, id, user)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAppRoleCannotBypassRowSecurity(t *testing.T) {
	d := testdb.New(t)
	var super, bypass bool
	err := d.App.QueryRow(context.Background(),
		`select rolsuper, rolbypassrls from pg_roles where rolname = current_user`).Scan(&super, &bypass)
	if err != nil {
		t.Fatal(err)
	}
	if super || bypass {
		t.Fatalf("runtime role must not bypass RLS (super=%v bypass=%v)", super, bypass)
	}
}

// Row-level security confines every query to the acting user (SEC-ISO-10).
func TestRowSecurityScopesToTheUser(t *testing.T) {
	ctx := context.Background()
	d := testdb.New(t)
	s := store.New(d.App)
	a, b := newUser(t, d, "alice"), newUser(t, d, "bob")
	noteA, noteB := adminNote(t, d, a), adminNote(t, d, b)

	// Without a user context nothing is visible, even though rows exist.
	var n int
	if err := d.App.QueryRow(ctx, `select count(*) from notes`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("no context: count=%d err=%v", n, err)
	}

	err := s.InUserTx(ctx, a, func(tx *store.UserTx) error {
		var ids []uuid.UUID
		rows, err := tx.Tx.Query(ctx, `select id from notes`)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		if len(ids) != 1 || ids[0] != noteA {
			return fmt.Errorf("alice sees %v, want only %v", ids, noteA)
		}
		// Reading or changing bob's note by id finds nothing.
		if tag, err := tx.Tx.Exec(ctx, `update notes set version = version + 1 where id = $1`, noteB); err != nil || tag.RowsAffected() != 0 {
			return fmt.Errorf("update of foreign note: rows=%d err=%v", tag.RowsAffected(), err)
		}
		// Writing a row for another user is refused by the policy's check clause. A savepoint
		// keeps the refusal from aborting the surrounding transaction.
		sp, err := tx.Tx.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = sp.Rollback(ctx) }()
		if _, err := sp.Exec(ctx, `insert into notes (id, user_id, created_at) values ($1, $2, now())`, uuid.New(), b); err == nil {
			return fmt.Errorf("insert for another user was allowed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUnknownUserIsNotFound(t *testing.T) {
	d := testdb.New(t)
	err := store.New(d.App).InUserTx(context.Background(), uuid.New(), func(*store.UserTx) error { return nil })
	if err != store.ErrNotFound {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestChangeSequenceIsGaplessUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	d := testdb.New(t)
	s := store.New(d.App)
	u := newUser(t, d, "alice")

	const writers = 25
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.InUserTx(ctx, u, func(tx *store.UserTx) error {
				return tx.Change(ctx, "note", uuid.New(), "upsert", nil)
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.Admin.Query(ctx, `select seq from changes where user_id = $1 order by seq`, u)
	if err != nil {
		t.Fatal(err)
	}
	seqs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatal(err)
	}
	if len(seqs) != writers || !sort.SliceIsSorted(seqs, func(i, j int) bool { return seqs[i] < seqs[j] }) {
		t.Fatalf("seqs = %v", seqs)
	}
	for i, v := range seqs {
		if v != int64(i+1) {
			t.Fatalf("gap in change sequence: %v", seqs)
		}
	}
}

func TestNotifyOnCommitOnly(t *testing.T) {
	ctx := context.Background()
	d := testdb.New(t)
	s := store.New(d.App)
	u := newUser(t, d, "alice")

	listener, err := pgx.Connect(ctx, d.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close(ctx) }()
	if _, err := listener.Exec(ctx, `listen `+store.ChannelChanges); err != nil {
		t.Fatal(err)
	}

	// A rolled-back transaction must not notify.
	_ = s.InUserTx(ctx, u, func(tx *store.UserTx) error {
		_ = tx.Change(ctx, "note", uuid.New(), "upsert", nil)
		return fmt.Errorf("boom")
	})
	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if n, err := listener.WaitForNotification(waitCtx); err == nil {
		t.Fatalf("notification from rolled-back transaction: %v", n)
	}

	// The failed transaction also did not consume a sequence number.
	if err := s.InUserTx(ctx, u, func(tx *store.UserTx) error {
		return tx.Change(ctx, "note", uuid.New(), "upsert", nil)
	}); err != nil {
		t.Fatal(err)
	}
	waitCtx2, cancel2 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel2()
	n, err := listener.WaitForNotification(waitCtx2)
	if err != nil {
		t.Fatal(err)
	}
	if want := u.String() + ":1"; n.Payload != want {
		t.Fatalf("payload %q, want %q", n.Payload, want)
	}
}

func TestDeletingCategoryReturnsNotesToInbox(t *testing.T) {
	ctx := context.Background()
	d := testdb.New(t)
	u := newUser(t, d, "alice")
	page, cat, note := uuid.New(), uuid.New(), uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`insert into pages (id, user_id, name, position) values ($1,$2,'Work','a')`, []any{page, u}},
		{`insert into categories (id, user_id, page_id, name, position) values ($1,$2,$3,'Todo','a')`, []any{cat, u, page}},
		{`insert into notes (id, user_id, category_id, position, created_at) values ($1,$2,$3,'m',now())`, []any{note, u, cat}},
	} {
		if _, err := d.Admin.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Admin.Exec(ctx, `delete from pages where id = $1`, page); err != nil {
		t.Fatalf("deleting a page must not be blocked by its notes: %v", err)
	}
	var category *uuid.UUID
	var position *string
	if err := d.Admin.QueryRow(ctx, `select category_id, position from notes where id = $1`, note).Scan(&category, &position); err != nil {
		t.Fatal(err)
	}
	if category != nil || position != nil {
		t.Fatalf("note must be back in the Inbox with no position: %v %v", category, position)
	}
}

func TestCrossUserReferencesAreRefused(t *testing.T) {
	ctx := context.Background()
	d := testdb.New(t)
	a, b := newUser(t, d, "alice"), newUser(t, d, "bob")
	page, cat := uuid.New(), uuid.New()
	if _, err := d.Admin.Exec(ctx, `insert into pages (id, user_id, name, position) values ($1,$2,'p','a')`, page, a); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `insert into categories (id, user_id, page_id, name, position) values ($1,$2,$3,'c','a')`, cat, a, page); err != nil {
		t.Fatal(err)
	}
	// Bob cannot put his note into Alice's category (SEC-ISO-4): the composite foreign key refuses it.
	_, err := d.Admin.Exec(ctx, `insert into notes (id, user_id, category_id, position, created_at) values ($1,$2,$3,'m',now())`, uuid.New(), b, cat)
	if err == nil {
		t.Fatal("a note referencing another user's category was accepted")
	}
}

// The runtime role is confined: no schema changes (SEC-OPS-5), an append-only audit log (SEC-AUD-2),
// and the bot's role sees nothing of Core's tables (SEC-OPS-5, BOT-B5).
func TestRuntimeRolesAreConfined(t *testing.T) {
	ctx := context.Background()
	d := testdb.New(t)
	if _, err := d.App.Exec(ctx, `create table sneaky (x int)`); err == nil {
		t.Error("the runtime role can create tables")
	}
	if _, err := d.App.Exec(ctx, `alter table users add column pwned text`); err == nil {
		t.Error("the runtime role can alter tables")
	}
	if _, err := d.App.Exec(ctx, `insert into audit_log (actor_kind, action) values ('system', 'test')`); err != nil {
		t.Errorf("the runtime role must append to the audit log: %v", err)
	}
	if _, err := d.App.Exec(ctx, `update audit_log set action = 'forged'`); err == nil {
		t.Error("the audit log can be modified by the application")
	}
	if _, err := d.App.Exec(ctx, `delete from audit_log`); err == nil {
		t.Error("the audit log can be emptied by the application")
	}

	bot, err := pgx.Connect(ctx, strings.Replace(d.AppURL, "nk_app:nk_app_dev", "nk_bot_matrix:nk_bot_dev", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bot.Close(ctx) }()
	for _, table := range []string{"users", "notes", "bot_credentials", "sessions"} {
		if _, err := bot.Exec(ctx, "select * from public."+table); err == nil {
			t.Errorf("the bot's role can read Core's table %s", table)
		}
	}
	if _, err := bot.Exec(ctx, `create table matrix_bot.mine (x int)`); err != nil {
		t.Errorf("the bot's role must own its own schema: %v", err)
	}
}

// Writes to note parts without a user context fail loudly instead of silently skipping the search
// index (migration 0005). The superuser bypasses row-level security, so only the trigger guards it.
func TestSearchIndexingRefusesToRunWithoutAUserContext(t *testing.T) {
	ctx := context.Background()
	d := testdb.New(t)
	user := newUser(t, d, "alice")
	note := adminNote(t, d, user)
	_, err := d.Admin.Exec(ctx, `insert into note_parts (id, user_id, note_id, ordinal, kind, text, attach_reason, created_at)
		values ($1, $2, $3, 0, 'text', 'x', 'first', now())`, uuid.New(), user, note)
	if err == nil || !strings.Contains(err.Error(), "app.user_id") {
		t.Fatalf("expected the trigger to refuse a part written without a user context, got %v", err)
	}
	// With the context set, the same write is indexed.
	tx, err := d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select set_config('app.user_id', $1, true)`, user.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `insert into note_parts (id, user_id, note_id, ordinal, kind, text, attach_reason, created_at)
		values ($1, $2, $3, 0, 'text', 'indexed text', 'first', now())`, uuid.New(), user, note); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.Admin.QueryRow(ctx, `select count(*) from note_search where body = 'indexed text'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("indexed rows: %d %v", n, err)
	}
}
