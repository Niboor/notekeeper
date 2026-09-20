//go:build integration

package server_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/app"
	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/realtime"
	"github.com/Niboor/notekeeper/core/internal/server"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/testdb"
)

// A second Core process on the same database and keys serves a session created by the first, and
// keeps serving it after the first is gone: no state lives in a process (AUTH-C5, NFR-D1, NFR-D2, SEC-AUTH-15).
func TestAnyReplicaServesAnySessionAndSessionsSurviveRestarts(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	u.note("", "written through replica A", nil)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc2, err := app.NewServices(s.cfg, s.st, log)
	if err != nil {
		t.Fatal(err)
	}
	hub2 := realtime.NewHub(s.db.AppURL, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub2.Run(ctx)
	defer hub2.Shutdown()
	routers2, err := server.NewRouters(server.Deps{Config: s.cfg, Log: log, Store: s.st, Accounts: svc2.Accounts, Bots: svc2.Bots, Notes: svc2.Notes, Board: svc2.Board,
		Blobs: svc2.Blobs, Ingest: svc2.Ingest, Outbox: svc2.Outbox, Shares: svc2.Shares, Reminders: svc2.Reminders, Export: svc2.Export, Hub: hub2})
	if err != nil {
		t.Fatal(err)
	}
	b := httptest.NewServer(routers2.User)
	defer b.Close()
	viaB := &client{s: &stack{t: t, user: b, cfg: s.cfg}, cookies: u.c.cookies}
	if res := viaB.do("GET", "/api/v1/inbox/notes", nil); res.Status != 200 || !strings.Contains(string(res.Body), "written through replica A") {
		t.Fatalf("replica B: %d %s", res.Status, res.Body)
	}
	// Renewal on B produces cookies that A accepts, and a sign-out on B ends the session on A (no cache in either process).
	if res := viaB.do("POST", "/api/v1/auth/refresh", nil); res.Status != 200 {
		t.Fatalf("refresh on B: %d %s", res.Status, res.Body)
	}
	for k, v := range viaB.cookies {
		u.c.cookies[k] = v
	}
	if res := u.c.do("GET", "/api/v1/me", nil); res.Status != 200 {
		t.Fatalf("A after B renewed: %d", res.Status)
	}
	// Several tabs renewing at once with the same refresh token all succeed, and the session lives on (SEC-AUTH-15).
	shared := map[string]string{}
	for k, v := range u.c.cookies {
		shared[k] = v
	}
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &client{s: s.newClient().s, cookies: map[string]string{}}
			for k, v := range shared {
				c.cookies[k] = v
			}
			codes[i] = c.do("POST", "/api/v1/auth/refresh", nil).Status
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != 200 {
			t.Errorf("concurrent refresh %d answered %d", i, code)
		}
	}
	// A restart of the first process changes nothing: a new one on the same database serves the session too.
	if res := viaB.do("GET", "/api/v1/me", nil); res.Status != 200 {
		t.Fatalf("after all that: %d", res.Status)
	}
	// Signing in again is a new session, never the old identifier (SEC-AUTH-9).
	first := u.c.cookies["__Host-nka"]
	other := s.newClient()
	other.login("alice")
	if other.cookies["__Host-nka"] == "" || other.cookies["__Host-nka"] == first || other.cookies["__Secure-nkr"] == u.c.cookies["__Secure-nkr"] {
		t.Fatal("a new sign-in reused session credentials")
	}
	if s.count(`select count(*) from sessions where user_id = (select id from users where username = 'alice')`) < 2 {
		t.Fatal("each sign-in must be its own session")
	}
}

// Every secret is stored only as a hash, or not at all; the database holds nothing that could be replayed
// (SEC-DATA-2, SEC-AUTH-12).
func TestSecretsAreOnlyStoredAsHashes(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("gina", key)
	n := ch.note("", "shared", nil)
	link := ch.share(n.ID, "1d")
	pending, err := s.svc.Accounts.CreateUser(t.Context(), storeActor(), accountsCreate("hank"))
	if err != nil {
		t.Fatal(err)
	}
	act, err := s.svc.Accounts.IssueActivation(t.Context(), storeActor(), pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	code := func() string {
		insts, _ := s.svc.Bots.ListInstances(t.Context())
		var uid uuid.UUID
		_ = s.db.Admin.QueryRow(t.Context(), `select id from users where username = 'gina'`).Scan(&uid)
		pc, err := s.svc.Bots.CreatePairingCode(t.Context(), uid, "", &insts[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		return pc.Code
	}()
	parts := strings.Split(key, ".") // nkb.<client id>.<secret>
	secrets := map[string]string{
		"password": password, "activation token": act.Token, "pairing code": code, "bot secret": parts[len(parts)-1],
		"share token": link.token(t), "access token": ch.c.cookies["__Host-nka"], "refresh token": ch.c.cookies["__Secure-nkr"],
	}
	rows, err := s.db.Admin.Query(t.Context(), `select table_name from information_schema.tables where table_schema = 'public' and table_type = 'BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for what, secret := range secrets {
		if secret == "" {
			t.Fatalf("no %s to look for", what)
		}
		for _, table := range tables {
			if s.count(`select count(*) from `+table+` t where row_to_json(t)::text like '%' || $1 || '%'`, secret) != 0 {
				t.Errorf("the %s is stored in clear in %s", what, table)
			}
		}
	}
	var hash string
	_ = s.db.Admin.QueryRow(t.Context(), `select password_hash from users where username = 'gina'`).Scan(&hash)
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("password hash: %.20s", hash)
	}
}

// Every action of the administrator, and every security event of a person, leaves an entry without content
// (SEC-ADM-3, SEC-AUD-1, AUTH-B8, NFR-S6).
func TestSecurityEventsAreAudited(t *testing.T) {
	s := newStack(t)
	s.makeUser("root", true)
	admin := s.newClient()
	admin.login("root")
	var created struct{ ID string }
	admin.do("POST", "/api/v1/admin/users", map[string]any{"username": "ivan"}).JSON(t, &created)
	admin.do("POST", "/api/v1/admin/users/"+created.ID+"/activation-link", nil)
	admin.do("PATCH", "/api/v1/admin/users/"+created.ID, map[string]any{"disabled": true})
	admin.do("PATCH", "/api/v1/admin/users/"+created.ID, map[string]any{"disabled": false})
	admin.do("PATCH", "/api/v1/admin/users/"+created.ID, map[string]any{"quota_bytes": 12345})
	var inst struct{ ID string }
	admin.do("POST", "/api/v1/admin/bot-instances", map[string]any{"type": "matrix", "name": "audited", "identity_domain": "example.org"}).JSON(t, &inst)
	var cred struct{ ID string }
	admin.do("POST", "/api/v1/admin/bot-instances/"+inst.ID+"/credentials", map[string]any{"scopes": []string{"ingest"}}).JSON(t, &cred)
	admin.do("DELETE", "/api/v1/admin/bot-instances/"+inst.ID+"/credentials/"+cred.ID, nil)
	admin.do("PATCH", "/api/v1/admin/bot-instances/"+inst.ID, map[string]any{"status": "disabled"})
	admin.do("DELETE", "/api/v1/admin/users/"+created.ID, nil)

	u := s.appUser("jane")
	s.newClient().do("POST", "/api/v1/auth/login", map[string]any{"username": "jane", "password": "not the password"})
	if _, err := s.db.Admin.Exec(t.Context(), `delete from auth_throttle`); err != nil { // the failed sign-in above made the account wait
		t.Fatal(err)
	}
	u.c.do("POST", "/api/v1/me/password", map[string]any{"current_password": password, "new_password": "another long passphrase 12"})
	n := u.note("", "n", nil)
	u.share(n.ID, "1d")
	u.c.do("POST", "/api/v1/share-links/revoke-all", nil)
	u.c.do("POST", "/api/v1/me/sessions/revoke-all", map[string]any{"keep_current": true})

	got := map[string]int{}
	rows, err := s.db.Admin.Query(t.Context(), `select action, count(*) from audit_log group by action`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		var c int
		_ = rows.Scan(&a, &c)
		got[a] = c
	}
	rows.Close()
	for _, want := range []string{"user.created", "user.activation_link_issued", "user.disabled", "user.enabled", "user.quota_changed", "bot.instance_created",
		"bot.credential_created", "bot.credential_disabled", "bot.instance_disabled", "user.deletion_requested", "login.succeeded", "login.failed",
		"password.changed", "share.created", "share.revoked_all", "session.revoke_all"} {
		if got[want] == 0 {
			t.Errorf("no audit entry for %s (have %v)", want, got)
		}
	}
	// The admin's own entries carry the admin as the actor.
	if s.count(`select count(*) from audit_log where action in ('user.created', 'user.disabled', 'user.quota_changed', 'bot.instance_created') and actor_kind = 'admin' and actor_id is not null`) != 4 {
		t.Fatal("admin actions must name the admin as their actor")
	}
	// And nothing in an entry is content.
	if s.count(`select count(*) from audit_log where detail::text ~* '(password|passphrase|token|secret)'`) != 0 {
		t.Fatal("an audit entry carries a secret")
	}
}

// A request id set by a bot travels into Core's logs, so one chat message can be followed end to end (NFR-O1).
func TestRequestIdsAreCorrelatedFromTheBot(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("kate", key)
	ev := map[string]any{"event_id": "$corr", "kind": "message_created", "sender": ch.ext, "conversation": ch.conv, "message_id": "$corr",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "parts": []map[string]any{text("traced")}}
	res := s.botDoWith(key, "POST", "/bot/v1/events", ev, map[string]string{"X-Request-ID": "mx-trace-abc123"})
	if res.Status != 200 || res.Header.Get("X-Request-ID") != "mx-trace-abc123" {
		t.Fatalf("request id not echoed: %d %v", res.Status, res.Header)
	}
	if !strings.Contains(s.logs.String(), "mx-trace-abc123") {
		t.Fatal("the bot's request id does not appear in Core's logs")
	}
	// On the user API a client cannot choose it (that would let anyone forge a trace).
	r2 := ch.c.doWith("GET", "/api/v1/me", nil, func(r *httpRequest) { r.Header.Set("X-Request-ID", "forged-by-a-browser") })
	if r2.Header.Get("X-Request-ID") == "forged-by-a-browser" {
		t.Fatal("a browser chose its own request id")
	}
}

// A second chat platform needs no change in Core: register an instance of another type, link identities on
// both platforms, and notes and reminders keep their origin (NFR-X1, NFR-X2).
func TestASecondChatPlatformNeedsNoCoreChanges(t *testing.T) {
	s := newStack(t)
	mkey := s.makeBot("matrix-main", "example.org")
	u := s.appUser("lena")
	ctx := context.Background()
	inst, err := s.svc.Bots.CreateInstance(ctx, store.Actor{Kind: "system"}, bots.CreateInstanceInput{Type: "signal", Name: "signal-main", IdentityDomain: "signal.example"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := s.svc.Bots.CreateCredential(ctx, store.Actor{Kind: "system"}, inst.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var uid uuid.UUID
	_ = s.db.Admin.QueryRow(ctx, `select id from users where username = 'lena'`).Scan(&uid)
	link := func(key, bot, sender, conv string) {
		id := s.instanceID(bot)
		bid := uuid.MustParse(id)
		pc, err := s.svc.Bots.CreatePairingCode(ctx, uid, "", &bid)
		if err != nil {
			t.Fatal(err)
		}
		if res := s.botDo(key, "POST", "/bot/v1/commands", map[string]any{"command": "link", "args": pc.Code, "sender": sender, "conversation": conv}); res.Status != 200 {
			t.Fatalf("link: %d %s", res.Status, res.Body)
		}
	}
	link(mkey, "matrix-main", "@lena:example.org", "!m")
	link(cred.Bearer, "signal-main", "+3212345678@signal.example", "signal-chat")
	send := func(key, sender, conv, id, msg string) {
		res := s.botDo(key, "POST", "/bot/v1/events", map[string]any{"event_id": id, "kind": "message_created", "sender": sender, "conversation": conv, "message_id": id,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "parts": []map[string]any{text(msg)}})
		if res.Status != 200 {
			t.Fatalf("event: %d %s", res.Status, res.Body)
		}
	}
	send(mkey, "@lena:example.org", "!m", "$1", "from matrix")
	send(cred.Bearer, "+3212345678@signal.example", "signal-chat", "sig-1", "from signal")
	origins := map[string]string{}
	for _, n := range u.inbox() {
		var full struct {
			Parts []struct {
				Text          *string `json:"text"`
				SourceBotType *string `json:"source_bot_type"`
			} `json:"parts"`
		}
		u.get("/api/v1/notes/"+n.ID, &full)
		if full.Parts[0].Text != nil && full.Parts[0].SourceBotType != nil {
			origins[*full.Parts[0].Text] = *full.Parts[0].SourceBotType
		}
	}
	if origins["from matrix"] != "matrix" || origins["from signal"] != "signal" {
		t.Fatalf("origins: %v", origins)
	}
	// Reminders go to the chosen chats of either platform, each through its own bot instance.
	var ids struct {
		Items []struct{ ID, BotType string }
	}
	u.get("/api/v1/me/identities", &ids)
	for _, i := range ids.Items {
		u.c.do("PATCH", "/api/v1/me/identities/"+i.ID, map[string]any{"reminder_target": true})
	}
	note := u.note("", "both chats", nil)
	due := time.Now().Add(time.Hour)
	u.remind(note.ID, due, "")
	s.clockAt(due.Add(time.Minute))
	_, _ = s.svc.Reminders.FireDue(ctx)
	if s.count(`select count(distinct bot_instance_id) from bot_outbox where kind = 'reminder'`) != 2 {
		t.Fatal("the reminder must go to both platforms through their own instances")
	}
	// Each bot sees only its own items (SEC-BOT-1).
	if got := s.claim(cred.Bearer, ""); len(got) == 0 {
		t.Fatal("the signal bot has nothing to send")
	} else {
		for _, it := range got {
			if !strings.Contains(it.ExternalUserID, "signal") {
				t.Fatalf("the signal bot was handed %s", it.ExternalUserID)
			}
		}
	}
}

// Notes in the Trash stay for as long as their owner wants, however old, through every cleanup (CORE-N11, NFR-R3).
func TestTrashIsNeverPurgedAutomatically(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n := u.note("", "kept for years", nil)
	u.post("/api/v1/notes/"+n.ID+"/dismiss", nil, 200, nil)
	if _, err := s.db.Admin.Exec(t.Context(), `update notes set deleted_at = now() - interval '10 years'`); err != nil {
		t.Fatal(err)
	}
	if err := s.runHousekeeping(); err != nil {
		t.Fatal(err)
	}
	var tr notePageFull
	u.get("/api/v1/trash/notes", &tr)
	if len(tr.Items) != 1 || tr.Items[0].ID != n.ID {
		t.Fatalf("the old note was purged: %+v", tr)
	}
	// Only an explicit action removes it (NFR-R3).
	if r := u.c.do("DELETE", "/api/v1/notes/"+n.ID, nil); r.Status != 204 {
		t.Fatalf("permanent delete: %d", r.Status)
	}
}

// Fields the server controls cannot be set from a request, and nobody becomes an admin by any request
// (SEC-ISO-6, SEC-ISO-7, AUTH-U7).
func TestClientsCannotSetServerControlledFields(t *testing.T) {
	s := newStack(t)
	s.makeUser("root", true)
	u, other := s.appUser("mallory"), s.appUser("victim")
	foreign := other.note("", "victims", nil)
	var mine noteJSON
	res := u.c.do("POST", "/api/v1/notes", map[string]any{"parts": []map[string]any{{"type": "text", "text": "hi"}}, "user_id": s.lookupUser("victim").String(),
		"state": "deleted", "version": 99, "created_at": "2001-01-01T00:00:00Z", "deleted_at": "2001-01-01T00:00:00Z", "received_at": "2001-01-01T00:00:00Z"})
	if res.Status == 201 {
		res.JSON(t, &mine)
	}
	var owner uuid.UUID
	var state string
	var version int
	var created time.Time
	if res.Status == 201 {
		_ = s.db.Admin.QueryRow(t.Context(), `select user_id, state, version, created_at from notes where id = $1`, mine.ID).Scan(&owner, &state, &version, &created)
		if owner != s.lookupUser("mallory") || state != "active" || version != 1 || created.Year() < 2020 {
			t.Fatalf("a request set server fields: owner %v state %s version %d created %v", owner, state, version, created)
		}
	} else if res.Status != 400 {
		t.Fatalf("create: %d", res.Status)
	}
	if u.c.do("GET", "/api/v1/notes/"+foreign.ID, nil).Status != 404 {
		t.Fatal("a foreign note became visible")
	}
	// Profile and account fields.
	u.c.do("PATCH", "/api/v1/me", map[string]any{"display_name": "Mal", "is_admin": true, "status": "active", "quota_bytes": 1 << 50, "id": uuid.NewString()})
	var admin bool
	var quota *int64
	_ = s.db.Admin.QueryRow(t.Context(), `select is_admin, quota_bytes from users u left join user_storage s on s.user_id = u.id where username = 'mallory'`).Scan(&admin, &quota)
	if admin || (quota != nil && *quota > 1<<40) {
		t.Fatalf("a request changed the role or quota: admin %v quota %v", admin, quota)
	}
	// No admin operation for a user, and no second bootstrap.
	for _, path := range []string{"/api/v1/admin/users", "/api/v1/admin/bot-instances"} {
		if r := u.c.do("GET", path, nil); r.Status != 403 {
			t.Errorf("%s for a user: %d", path, r.Status)
		}
	}
	if _, err := s.svc.Accounts.Bootstrap(t.Context(), "second-admin"); err == nil {
		t.Fatal("a second admin was created")
	}
	if s.count(`select count(*) from users where is_admin`) != 1 {
		t.Fatal("there must be exactly one admin")
	}
}

// An account has the documented fields, a unique username whatever its case, and nothing depends on an
// e-mail address (AUTH-U2).
func TestAccountsHaveTheDocumentedFields(t *testing.T) {
	s := newStack(t)
	s.makeUser("root", true)
	admin := s.newClient()
	admin.login("root")
	create := func(body map[string]any) response { return admin.do("POST", "/api/v1/admin/users", body) }
	if r := create(map[string]any{"username": "Nora", "display_name": "Nora N", "email": "nora@example.org", "timezone": "Europe/Brussels"}); r.Status != 201 {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	for _, dup := range []string{"nora", "NORA", " Nora "} {
		if r := create(map[string]any{"username": dup}); r.Status != 409 {
			t.Errorf("duplicate username %q: %d", dup, r.Status)
		}
	}
	for _, bad := range []string{"", "has space", "way-too-long-" + strings.Repeat("x", 40), "../etc"} {
		if r := create(map[string]any{"username": bad}); r.Status != 400 {
			t.Errorf("invalid username %q: %d", bad, r.Status)
		}
	}
	var name, tz string
	var id uuid.UUID
	var email *string
	if err := s.db.Admin.QueryRow(t.Context(), `select id, display_name, timezone, email from users where username = 'nora'`).Scan(&id, &name, &tz, &email); err != nil {
		t.Fatal(err)
	}
	if id == uuid.Nil || name != "Nora N" || tz != "Europe/Brussels" || email == nil {
		t.Fatalf("stored: %v %q %q %v", id, name, tz, email)
	}
}

// A pg_dump backup restored into a new database gives back a working installation, files included: the
// same sessions, notes, attachments, search, reminders and share links (NFR-R4, SEC-DATA-7).
func TestBackupAndRestoreIncludeAttachments(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	u := s.chatter("olga", key)
	file := randomBytes(900_000)
	n, att := u.noteWithFile("boarding pass", "pass.pdf", "application/pdf", file)
	u.remind(n.ID, time.Now().Add(time.Hour), "")
	link := u.share(n.ID, "1d")
	u.send("$m1", 0, text("a chat message"))

	restored := newStackOn(t, testdb.Restore(t, s.db), nil)
	// The person's session survives the restore: cookies from before still work, since sessions live in the database.
	c := &client{s: restored, cookies: u.c.cookies}
	if r := c.do("GET", "/api/v1/attachments/"+att, nil); r.Status != 200 || !bytes.Equal(r.Body, file) {
		t.Fatalf("attachment after restore: %d, %d bytes", r.Status, len(r.Body))
	}
	var inbox notePageFull
	if r := c.do("GET", "/api/v1/inbox/notes", nil); r.Status != 200 || !strings.Contains(string(r.Body), "a chat message") || !strings.Contains(string(r.Body), "boarding pass") {
		t.Fatalf("inbox after restore: %d %s", r.Status, r.Body)
	} else {
		r.JSON(t, &inbox)
	}
	if r := c.do("GET", "/api/v1/search?q=boarding", nil); r.Status != 200 || !strings.Contains(string(r.Body), n.ID) {
		t.Fatalf("search after restore: %d", r.Status)
	}
	if r := c.do("GET", "/api/v1/reminders", nil); r.Status != 200 || !strings.Contains(string(r.Body), "boarding pass") {
		t.Fatalf("reminders after restore: %d", r.Status)
	}
	if r := restored.publicDo(link.token(t), "GET", "/api/public/v1/share", nil); r.Status != 200 {
		t.Fatalf("share link after restore: %d", r.Status)
	}
	// The restored database still enforces its rules: row-level security holds for the runtime role.
	if _, err := restored.db.App.Exec(t.Context(), `update note_parts set text = 'x'`); err != nil {
		t.Fatal(err)
	}
	if restored.count(`select count(*) from note_parts where text = 'x'`) != 0 {
		t.Fatal("the restored database lost its row-level security")
	}
}
