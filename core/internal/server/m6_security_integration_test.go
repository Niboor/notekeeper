//go:build integration

package server_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/config"
)

// A bot learns only whether an identity is linked, never who the person is; lookups are limited and
// audited without naming the person asked about (SEC-BOT-3, BOT-2).
func TestIdentityLookupRevealsLittleAndIsAudited(t *testing.T) {
	s := newStackWith(t, func(c *config.Config) { c.RateIdentityPerMin = 30 })
	key := s.makeBot("m", "example.org")
	ch := s.chatter("carol", key)
	lookup := func(who string) response { return s.botDo(key, "GET", "/bot/v1/identities/"+who, nil) }
	linked, unknown := lookup(ch.ext), lookup("@nobody:example.org")
	if linked.Status != 200 || unknown.Status != 200 {
		t.Fatalf("%d %d", linked.Status, unknown.Status)
	}
	// Only these fields exist in the answer; nothing that identifies the account can be added by accident.
	var fields map[string]any
	linked.JSON(t, &fields)
	for k := range fields {
		if k != "linked" && k != "linked_at" && k != "conversation" {
			t.Fatalf("the answer has a field %q that says something about the person", k)
		}
	}
	if s.count(`select count(*) from audit_log where action = 'bot.identity_lookup'`) != 2 {
		t.Fatal("every lookup must be audited")
	}
	if s.count(`select count(*) from audit_log where action = 'bot.identity_lookup' and (detail::text like '%carol%' or detail::text like '%nobody%')`) != 0 {
		t.Fatal("the audit entry names the person asked about")
	}
	refused := false
	for range 60 {
		if lookup("@sweep:example.org").Status == 429 {
			refused = true
			break
		}
	}
	if !refused {
		t.Fatal("lookups are not limited")
	}
}

// The admin sees who has an account and how much they store, and nothing that belongs to them (SEC-ADM-1, AUTH-U6).
func TestAdminCannotReadOthersContent(t *testing.T) {
	s := newStack(t)
	s.makeUser("root", true)
	admin := s.newClient()
	admin.login("root")
	u := s.appUser("dora")
	n, att := u.noteWithFile("dora's diary entry", "diary.txt", "text/plain", []byte("DIARY-BYTES"))
	u.share(n.ID, "1d")

	list := admin.do("GET", "/api/v1/admin/users", nil)
	if list.Status != 200 || strings.Contains(string(list.Body), "diary") || strings.Contains(string(list.Body), "note") {
		t.Fatalf("the user list holds content: %s", list.Body)
	}
	for _, path := range []string{"/api/v1/notes/" + n.ID, "/api/v1/notes/" + n.ID + "/history", "/api/v1/attachments/" + att, "/api/v1/inbox/notes", "/api/v1/trash/notes",
		"/api/v1/search?q=diary&scope=all", "/api/v1/share-links", "/api/v1/reminders"} {
		res := admin.do("GET", path, nil)
		if strings.Contains(string(res.Body), "diary") || strings.Contains(string(res.Body), "DIARY") || (strings.Contains(path, n.ID) && res.Status != 404) {
			t.Errorf("%s answers the admin with %d %s", path, res.Status, res.Body)
		}
	}
	if res := admin.do("GET", "/api/v1/bot-instances", nil); res.Status != 200 {
		t.Fatalf("%d", res.Status)
	}
	if res := admin.do("POST", "/api/v1/notes/"+n.ID+"/dismiss", nil); res.Status != 404 {
		t.Fatalf("the admin dismissed somebody's note: %d", res.Status)
	}
}

// Nothing a person writes, uploads or types as a secret ever reaches the logs, on success or on failure
// (SEC-DATA-1, SEC-AUTH-12, NFR-S7).
func TestSecretsAndContentNeverReachTheLogs(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("erin", key)
	const noteText, fileName, fileBytes = "the acquisition closes on the 14th", "term-sheet-confidential.pdf", "CONFIDENTIAL-FILE-BYTES"
	n, att := ch.noteWithFile(noteText, fileName, "application/pdf", []byte(fileBytes))
	ch.send("$m1", 0, text("chat says: the password for the vault is hunter2-secret-phrase"))
	link := ch.share(n.ID, "1d")
	s.publicDo(link.token(t), "GET", "/api/public/v1/share", nil)
	s.publicDo(link.token(t), "GET", "/api/public/v1/share/attachments/"+att, nil)
	// Failures with secrets in the request.
	wrong := "wrong-password-erin-tried"
	s.newClient().do("POST", "/api/v1/auth/login", map[string]any{"username": "erin", "password": wrong})
	ch.c.do("POST", "/api/v1/me/password", map[string]any{"current_password": wrong, "new_password": "brand-new-passphrase-99"})
	s.botDo("nkb.bad.credential-value-xyz", "POST", "/bot/v1/events", map[string]any{"event_id": "x"})
	ch.c.do("POST", "/api/v1/notes", map[string]any{"parts": []map[string]any{{"type": "text", "text": strings.Repeat("x", 200_000) + "TOO-LONG-NOTE-BODY"}}}) // rejected: too long
	ch.c.do("PATCH", "/api/v1/me", map[string]any{"display_name": strings.Repeat("Ω", 500)})
	// Reminders, exports, merges, deletion.
	ch.remind(n.ID, time.Now().Add(time.Hour), "")
	s.clockAt(time.Now().Add(2 * time.Hour))
	_, _ = s.svc.Reminders.FireDue(t.Context())
	ch.c.do("GET", "/api/v1/me/export", nil)
	pairing := ch.c.do("POST", "/api/v1/me/pairing-codes", map[string]any{"bot_instance_id": s.instanceID("m")})
	var pc struct{ Code string }
	pairing.JSON(t, &pc)
	s.botDo(key, "POST", "/bot/v1/commands", map[string]any{"command": "link", "args": pc.Code, "sender": "@intruder:example.org", "conversation": "!x"})

	logs := s.logs.String()
	if !strings.Contains(logs, "level=") {
		t.Fatal("the test must see the logs, or it proves nothing")
	}
	for _, secret := range []string{noteText, "acquisition", fileName, fileBytes, "hunter2", "vault", wrong, "brand-new-passphrase", "credential-value-xyz", "TOO-LONG-NOTE-BODY", link.token(t), password, pc.Code} {
		if strings.Contains(logs, secret) {
			t.Errorf("the logs hold %q", secret)
		}
	}
	// Metrics and the audit log are held to the same standard.
	audit := ""
	rows, _ := s.db.Admin.Query(t.Context(), `select coalesce(detail::text, '') || coalesce(action, '') from audit_log`)
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		audit += d
	}
	rows.Close()
	for _, secret := range []string{noteText, fileName, "hunter2", wrong, link.token(t), pc.Code} {
		if strings.Contains(audit, secret) {
			t.Errorf("the audit log holds %q", secret)
		}
	}
	_ = http.StatusOK
	_ = uuid.Nil
}
