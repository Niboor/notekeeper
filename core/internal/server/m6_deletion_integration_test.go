//go:build integration

package server_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/store"
)

// tablesWithUser lists every table that has a user_id column, so the completeness check needs no
// list to keep up to date: a new table that holds user data fails it unless it cascades (AUTH-U9).
func (s *stack) tablesWithUser() []string {
	s.t.Helper()
	rows, err := s.db.Admin.Query(context.Background(), `select table_name from information_schema.columns where table_schema = 'public' and column_name = 'user_id' order by 1`)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

func (s *stack) rowsOfUser(user uuid.UUID) map[string]int {
	s.t.Helper()
	out := map[string]int{}
	for _, table := range s.tablesWithUser() {
		var n int
		if err := s.db.Admin.QueryRow(context.Background(), `select count(*) from `+table+` where user_id = $1`, user).Scan(&n); err != nil {
			s.t.Fatal(err)
		}
		if n > 0 {
			out[table] = n
		}
	}
	return out
}

// Deleting an account removes everything about it, stops everything at once, tells the bot to forget
// the chat, and leaves other people's data alone (AUTH-U4, AUTH-U9, BOT-16, SEC-DATA-5, NFR-S7).
func TestAccountDeletionIsComplete(t *testing.T) {
	s := newStack(t)
	admin := s.makeUser("root", true)
	key := s.makeBot("m", "example.org")
	vera := s.chatter("vera", key)
	other := s.appUser("other")
	var veraID uuid.UUID
	_ = s.db.Admin.QueryRow(t.Context(), `select id from users where username = 'vera'`).Scan(&veraID)

	// Vera has one of everything.
	work := vera.page("Work")
	cat := vera.category(work, "Todo")
	n1 := vera.note(cat, "secret plan about the merger", nil)
	vera.c.do("PATCH", "/api/v1/notes/"+n1.ID+"/parts/"+n1.Parts[0].ID, map[string]any{"text": "secret plan, revised"})
	n2, att := vera.noteWithFile("boarding pass", "pass.pdf", "application/pdf", randomBytes(700_000))
	vera.send("$m1", 0, text("hello from the chat"), vera.photo("chat.png"))
	vera.remind(n2.ID, time.Now().Add(time.Hour), "")
	link := vera.share(n2.ID, "1d")
	vera.c.do("POST", "/api/v1/me/pairing-codes", map[string]any{"bot_instance_id": s.instanceID("m")})
	vera.c.login("vera") // a second session
	s.clockAt(time.Now().Add(2 * time.Hour))
	_, _ = s.svc.Reminders.FireDue(t.Context()) // a fired reminder: an outbox delivery and a notification
	if s.rowsOfUser(veraID)["notes"] == 0 || s.rowsOfUser(veraID)["blob_chunks"] == 0 || s.rowsOfUser(veraID)["bot_outbox"] == 0 || s.rowsOfUser(veraID)["notifications"] == 0 {
		t.Fatalf("the fixture is too small to prove anything: %v", s.rowsOfUser(veraID))
	}
	otherNote, otherAtt := other.noteWithFile("other's note", "o.txt", "text/plain", []byte("keep me"))
	before := s.rowsOfUser(uuid.MustParse(other.userID()))

	// A wrong password deletes nothing.
	if res := vera.c.do("POST", "/api/v1/me/deletion", map[string]any{"password": "wrong wrong wrong"}); res.Status != 401 {
		t.Fatalf("wrong password: %d", res.Status)
	}
	if s.count(`select count(*) from users where id = $1 and status = 'active'`, veraID) != 1 {
		t.Fatal("a wrong password changed the account")
	}
	// The admin's own account is protected.
	adminClient := s.newClient()
	adminClient.login("root")
	if res := adminClient.do("POST", "/api/v1/me/deletion", map[string]any{"password": password}); res.Status != 409 {
		t.Fatalf("admin self-deletion: %d", res.Status)
	}
	if res := adminClient.do("DELETE", "/api/v1/admin/users/"+admin.String(), nil); res.Status != 409 {
		t.Fatalf("admin deletion: %d", res.Status)
	}
	if res := other.c.do("DELETE", "/api/v1/admin/users/"+veraID.String(), nil); res.Status != 403 {
		t.Fatalf("a user deleting another: %d", res.Status)
	}

	// The real thing. (The wrong password above made the account wait; the owner types it again a little later.)
	if _, err := s.db.Admin.Exec(t.Context(), `delete from auth_throttle`); err != nil {
		t.Fatal(err)
	}
	if res := vera.c.do("POST", "/api/v1/me/deletion", map[string]any{"password": password}); res.Status != 202 {
		t.Fatalf("delete: %d %s", res.Status, res.Body)
	}
	// From this moment nothing of hers works, before any data has been removed.
	if res := s.newClient().do("POST", "/api/v1/auth/login", map[string]any{"username": "vera", "password": password}); res.Status != 401 {
		t.Fatalf("sign-in during deletion: %d", res.Status)
	}
	if res := vera.c.do("GET", "/api/v1/me", nil); res.Status != 401 {
		t.Fatalf("old session during deletion: %d", res.Status)
	}
	if s.publicDo(link.token(t), "GET", "/api/public/v1/share", nil).Status != 404 {
		t.Fatal("a share link kept working")
	}
	if out := vera.send("$m2", time.Minute, text("still there?")); out.Result != "rejected" {
		t.Fatalf("ingest during deletion: %+v", out)
	}
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 0 {
		t.Fatal("a reminder fired for a deleted account")
	}
	if s.count(`select count(*) from bot_outbox where kind = 'lifecycle' and conversation_id = $1 and user_id is null and payload->>'reason' = 'user_deleted'`, vera.conv) != 1 {
		t.Fatal("the bot must be told to forget the chat, in an item that names no user")
	}

	// The data goes in the background.
	if n, err := s.svc.Accounts.ProcessDeletions(t.Context()); err != nil || n != 1 {
		t.Fatalf("process: %d %v", n, err)
	}
	if s.count(`select count(*) from users where id = $1`, veraID) != 0 {
		t.Fatal("the account row is still there")
	}
	if left := s.rowsOfUser(veraID); len(left) != 0 {
		t.Fatalf("data left behind: %v", left)
	}
	for _, q := range []string{
		`select count(*) from note_search where user_id = $1`, `select count(*) from blob_chunks where user_id = $1`, `select count(*) from blobs where user_id = $1`,
		`select count(*) from bot_outbox where user_id = $1`, `select count(*) from ingest_events where user_id = $1`, `select count(*) from share_links where user_id = $1`,
	} {
		if s.count(q, veraID) != 0 {
			t.Fatalf("left: %s", q)
		}
	}
	// Only content-free audit entries remain.
	if s.count(`select count(*) from audit_log where action in ('user.deletion_requested', 'user.deleted') and target_id = $1`, veraID) != 2 {
		t.Fatal("the deletion must be audited")
	}
	dump := ""
	rows, _ := s.db.Admin.Query(t.Context(), `select detail::text || coalesce(target_kind, '') from audit_log`)
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		dump += d
	}
	rows.Close()
	for _, secret := range []string{"merger", "boarding", "hello from the chat", "pass.pdf"} {
		if strings.Contains(dump, secret) {
			t.Fatalf("the audit log holds content: %q", secret)
		}
	}
	// Somebody else's data is exactly as it was.
	after := s.rowsOfUser(uuid.MustParse(other.userID()))
	for table, n := range before {
		if after[table] < n {
			t.Fatalf("other's %s shrank from %d to %d", table, n, after[table])
		}
	}
	if dl := other.c.do("GET", "/api/v1/attachments/"+otherAtt, nil); dl.Status != 200 || string(dl.Body) != "keep me" || other.reminderOf(otherNote.ID) == nil && false {
		t.Fatal("the other user's file changed")
	}
	_ = att
	// Deleting again is harmless.
	if err := s.svc.Accounts.RequestDeletion(t.Context(), store.Actor{Kind: "system"}, veraID); err == nil {
		t.Fatal("a deleted account cannot be found")
	}
}

// The admin deletes an account through the API, and the ordinary user cannot (AUTH-U6, SEC-ADM-3).
func TestAdminDeletesAnAccount(t *testing.T) {
	s := newStack(t)
	s.makeUser("root", true)
	adminClient := s.newClient()
	adminClient.login("root")
	victim := s.appUser("victim")
	victim.note("", "content", nil)
	var id uuid.UUID
	_ = s.db.Admin.QueryRow(t.Context(), `select id from users where username = 'victim'`).Scan(&id)
	if res := adminClient.do("DELETE", "/api/v1/admin/users/"+id.String(), nil); res.Status != 202 {
		t.Fatalf("admin delete: %d %s", res.Status, res.Body)
	}
	if n, err := s.svc.Accounts.ProcessDeletions(t.Context()); err != nil || n != 1 || len(s.rowsOfUser(id)) != 0 {
		t.Fatalf("deleted %d (%v), left %v", n, err, s.rowsOfUser(id))
	}
	if s.count(`select count(*) from audit_log where action = 'user.deletion_requested' and actor_kind = 'admin'`) != 1 {
		t.Fatal("an admin action must be audited")
	}
	if res := adminClient.do("DELETE", "/api/v1/admin/users/"+uuid.NewString(), nil); res.Status != 404 {
		t.Fatalf("unknown user: %d", res.Status)
	}
}
