//go:build integration

package server_test

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type meBody struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"is_admin"`
}

type notePage struct {
	Items []struct {
		ID    string `json:"id"`
		State string `json:"state"`
		Parts []struct {
			Kind string  `json:"kind"`
			Text *string `json:"text"`
		} `json:"parts"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
	Total      *int64  `json:"total"`
}

// Also demonstrates: AUTH-C4.
func TestLoginCookiesAndCSRF(t *testing.T) {
	s := newStack(t)
	s.makeUser("alice", false)
	c := s.newClient()

	// A login without the CSRF header is refused even with the right password (SEC-AUTH-8).
	res := c.doWith("POST", "/api/v1/auth/login", map[string]any{"username": "alice", "password": password},
		func(r *http.Request) { r.Header.Del("X-Notekeeper-Client") })
	if res.Status != 403 || res.Code() != "csrf" {
		t.Fatalf("no client header: %d %s", res.Status, res.Body)
	}
	// So is a cross-site request that carries the header.
	res = c.doWith("POST", "/api/v1/auth/login", map[string]any{"username": "alice", "password": password},
		func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	if res.Status != 403 {
		t.Fatalf("cross-site: %d", res.Status)
	}
	// Without Sec-Fetch-Site (older browsers) a foreign Origin is refused.
	res = c.doWith("POST", "/api/v1/auth/login", map[string]any{"username": "alice", "password": password},
		func(r *http.Request) { r.Header.Del("Sec-Fetch-Site"); r.Header.Set("Origin", "https://evil.example") })
	if res.Status != 403 {
		t.Fatalf("foreign origin: %d", res.Status)
	}

	res = c.do("POST", "/api/v1/auth/login", map[string]any{"username": "Alice", "password": password})
	if res.Status != 200 {
		t.Fatalf("login: %d %s", res.Status, res.Body)
	}
	// Cookie attributes (docs/design/03-auth.md section 2.2).
	seen := map[string]*http.Cookie{}
	for _, ck := range (&http.Response{Header: res.Header}).Cookies() {
		seen[ck.Name] = ck
	}
	access, refresh := seen["__Host-nka"], seen["__Secure-nkr"]
	if access == nil || refresh == nil {
		t.Fatalf("cookies: %v", res.Header["Set-Cookie"])
	}
	for _, ck := range []*http.Cookie{access, refresh} {
		if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode {
			t.Errorf("cookie %s must be HttpOnly, Secure, SameSite=Strict: %+v", ck.Name, ck)
		}
	}
	if access.Path != "/" || refresh.Path != "/api/v1/auth" {
		t.Errorf("cookie paths: %q %q", access.Path, refresh.Path)
	}
	if strings.Contains(string(res.Body), "nka.") || strings.Contains(string(res.Body), "nkr.") {
		t.Error("tokens must not appear in a web login response body")
	}

	var me meBody
	c.do("GET", "/api/v1/me", nil).JSON(t, &me)
	if me.Username != "alice" || me.IsAdmin {
		t.Fatalf("me: %+v", me)
	}
	// GET needs no CSRF header, but state-changing calls on a cookie session do.
	if res := c.doWith("POST", "/api/v1/auth/logout", nil, func(r *http.Request) { r.Header.Del("X-Notekeeper-Client") }); res.Status != 403 {
		t.Fatalf("logout without header: %d", res.Status)
	}
	if res := c.do("POST", "/api/v1/auth/logout", nil); res.Status != 204 {
		t.Fatalf("logout: %d %s", res.Status, res.Body)
	}
	if res := c.do("GET", "/api/v1/me", nil); res.Status != 401 {
		t.Fatalf("after logout: %d", res.Status)
	}
}

// Also demonstrates: AUTH-C10.
func TestSilentRenewalOverHTTP(t *testing.T) {
	s := newStack(t)
	s.makeUser("alice", false)
	c := s.newClient()
	c.login("alice")
	old := c.cookies["__Secure-nkr"]

	if res := c.do("POST", "/api/v1/auth/refresh", nil); res.Status != 200 {
		t.Fatalf("refresh: %d %s", res.Status, res.Body)
	}
	if c.cookies["__Secure-nkr"] == old {
		t.Fatal("refresh cookie must rotate")
	}
	if res := c.do("GET", "/api/v1/me", nil); res.Status != 200 {
		t.Fatalf("me after refresh: %d", res.Status)
	}
	// A refresh without any cookie is a clean 401 (the client then shows the login page).
	anon := s.newClient()
	if res := anon.do("POST", "/api/v1/auth/refresh", nil); res.Status != 401 {
		t.Fatalf("anonymous refresh: %d", res.Status)
	}
}

// Also demonstrates: AUTH-C3.
func TestNativeClientsGetTokensInTheBody(t *testing.T) {
	s := newStack(t)
	s.makeUser("alice", false)
	c := s.newClient()
	res := c.do("POST", "/api/v1/auth/login", map[string]any{"username": "alice", "password": password, "client_kind": "native"})
	if res.Status != 200 {
		t.Fatal(res.Status, string(res.Body))
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	res.JSON(t, &out)
	if out.AccessToken == "" || out.RefreshToken == "" || len(c.cookies) != 0 {
		t.Fatalf("native login: %+v cookies=%v", out, c.cookies)
	}
	// A bearer token works without cookies and without the CSRF header.
	bare := s.newClient()
	res = bare.doWith("GET", "/api/v1/me", nil, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+out.AccessToken) })
	if res.Status != 200 {
		t.Fatalf("bearer: %d", res.Status)
	}
}

func TestActivationOverHTTP(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	admin := s.makeUser("root", true)
	ac := s.newClient()
	ac.login("root")

	// The admin creates a user and issues an activation link; the user opens it.
	var created struct {
		ID string `json:"id"`
	}
	res := ac.do("POST", "/api/v1/admin/users", map[string]any{"username": "carol", "display_name": "Carol"})
	if res.Status != 201 {
		t.Fatalf("create user: %d %s", res.Status, res.Body)
	}
	res.JSON(t, &created)
	var link struct{ Token, Path string }
	res = ac.do("POST", "/api/v1/admin/users/"+created.ID+"/activation-link", nil)
	if res.Status != 201 {
		t.Fatalf("link: %d %s", res.Status, res.Body)
	}
	res.JSON(t, &link)
	if !strings.HasPrefix(link.Path, "/activate#") {
		t.Fatalf("the token must sit in the URL fragment: %q", link.Path)
	}

	uc := s.newClient()
	if res := uc.do("POST", "/api/v1/auth/activate", map[string]any{"token": link.Token, "password": "short"}); res.Status != 400 || res.Code() != "invalid_input" {
		t.Fatalf("weak password: %d %s", res.Status, res.Body)
	}
	res = uc.do("POST", "/api/v1/auth/activate", map[string]any{"token": link.Token, "password": password})
	if res.Status != 200 {
		t.Fatalf("activate: %d %s", res.Status, res.Body)
	}
	var me meBody
	uc.do("GET", "/api/v1/me", nil).JSON(t, &me)
	if me.Username != "carol" {
		t.Fatalf("signed in as %q", me.Username)
	}
	if res := s.newClient().do("POST", "/api/v1/auth/activate", map[string]any{"token": link.Token, "password": password}); res.Status != 400 {
		t.Fatalf("reused link: %d", res.Status)
	}
	// An ordinary user cannot use admin endpoints (403), and the audit log recorded the admin actions.
	if res := uc.do("GET", "/api/v1/admin/users", nil); res.Status != 403 {
		t.Fatalf("non-admin on admin route: %d", res.Status)
	}
	var n int
	_ = s.db.Admin.QueryRow(ctx, `select count(*) from audit_log where action in ('user.created','user.activation_link_issued') and actor_id = $1`, admin).Scan(&n)
	if n != 2 {
		t.Fatalf("admin actions audited: %d", n)
	}
}

// TestChatToInbox is the M1 exit criterion (F1, F2 at API level): a text message sent by a
// linked chat identity appears in the web Inbox, and the web app is told live.
// Also demonstrates: BOT-3, BOT-4, BOT-8.
func TestChatToInbox(t *testing.T) {
	s := newStack(t)
	s.makeUser("alice", false)
	c := s.newClient()
	c.login("alice")
	key := s.makeBot("matrix-main", "example.org")

	// An unknown sender is refused, told how to link once, and nothing is stored (AUTH-B6).
	ev := func(id, sender, text string) map[string]any {
		return map[string]any{"event_id": id, "kind": "message_created", "sender": sender, "conversation": "!room:example.org",
			"message_id": id, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "parts": []map[string]any{{"type": "text", "text": text}}}
	}
	var out struct {
		Result   string  `json:"result"`
		Code     *string `json:"code"`
		NoteID   *string `json:"note_id"`
		Feedback struct {
			React     *string `json:"react"`
			ReplyText *string `json:"reply_text"`
		} `json:"feedback"`
	}
	res := s.botDo(key, "POST", "/bot/v1/events", ev("$e0", "@alice:example.org", "hello"))
	res.JSON(t, &out)
	if res.Status != 200 || out.Result != "rejected" || out.Code == nil || *out.Code != "identity_unlinked" || out.Feedback.ReplyText == nil {
		t.Fatalf("unlinked: %d %s", res.Status, res.Body)
	}
	out.Feedback.ReplyText = nil
	s.botDo(key, "POST", "/bot/v1/events", ev("$e0b", "@alice:example.org", "hello again")).JSON(t, &out)
	if out.Feedback.ReplyText != nil {
		t.Fatal("the linking instructions must not be repeated within an hour")
	}

	// The web app opens its event stream first, then links a chat.
	var bots struct {
		Items []struct{ ID, Name string } `json:"items"`
	}
	c.do("GET", "/api/v1/bot-instances", nil).JSON(t, &bots)
	if len(bots.Items) != 1 {
		t.Fatalf("bot instances: %+v", bots)
	}
	sse := openSSE(t, s, c)
	defer sse.close()
	if ev := sse.next(t); ev.Name != "hello" {
		t.Fatalf("first event: %+v", ev)
	}

	var pc struct{ Code string }
	res = c.do("POST", "/api/v1/me/pairing-codes", map[string]any{"bot_instance_id": bots.Items[0].ID})
	if res.Status != 201 {
		t.Fatalf("pairing code: %d %s", res.Status, res.Body)
	}
	res.JSON(t, &pc)

	cmd := func(sender, args string) (ok bool, reply string) {
		var r struct {
			Ok       bool `json:"ok"`
			Feedback struct {
				ReplyText *string `json:"reply_text"`
			} `json:"feedback"`
		}
		s.botDo(key, "POST", "/bot/v1/commands", map[string]any{"command": "link", "args": args, "sender": sender, "conversation": "!room:example.org"}).JSON(t, &r)
		if r.Feedback.ReplyText != nil {
			reply = *r.Feedback.ReplyText
		}
		return r.Ok, reply
	}
	// The identity must belong to the bot's homeserver (SEC-BOT-5), and a wrong code is refused.
	if ok, _ := cmd("@alice:evil.example", pc.Code); ok {
		t.Fatal("identity from a foreign domain was linked")
	}
	if ok, _ := cmd("@alice:example.org", "AAAA-AAAA"); ok {
		t.Fatal("wrong code accepted")
	}
	if ok, reply := cmd("@alice:example.org", strings.ToLower(pc.Code)); !ok {
		t.Fatalf("link failed: %q", reply)
	}
	if ev := sse.until(t, "change"); !strings.Contains(ev.Data, `"identity"`) {
		t.Fatalf("linking must be pushed to the web app: %+v", ev)
	}
	// A code works once.
	if ok, _ := cmd("@bob:example.org", pc.Code); ok {
		t.Fatal("pairing code reused")
	}

	// Now messages become notes, and the web app hears about them.
	res = s.botDo(key, "POST", "/bot/v1/events", ev("$e1", "@alice:example.org", "buy milk"))
	res.JSON(t, &out)
	if out.Result != "created" || out.NoteID == nil || out.Feedback.React == nil || *out.Feedback.React != "ok" {
		t.Fatalf("created: %d %s", res.Status, res.Body)
	}
	changeEv := sse.until(t, "change")
	for !strings.Contains(changeEv.Data, *out.NoteID) && strings.Contains(changeEv.Data, `"notification"`) {
		changeEv = sse.until(t, "change") // the notice about the linked chat comes first
	}
	if !strings.Contains(changeEv.Data, *out.NoteID) {
		t.Fatalf("SSE change should name the note: %+v", changeEv)
	}

	var page notePage
	c.do("GET", "/api/v1/inbox/notes", nil).JSON(t, &page)
	if len(page.Items) != 1 || page.Items[0].ID != *out.NoteID || *page.Items[0].Parts[0].Text != "buy milk" || *page.Total != 1 {
		t.Fatalf("inbox: %+v", page)
	}

	// A replayed event returns the first answer and changes nothing (BOT-7).
	var again = out
	s.botDo(key, "POST", "/bot/v1/events", ev("$e1", "@alice:example.org", "buy milk")).JSON(t, &again)
	if again.Result != "created" || *again.NoteID != *out.NoteID {
		t.Fatalf("replay: %+v", again)
	}
	c.do("GET", "/api/v1/inbox/notes", nil).JSON(t, &page)
	if len(page.Items) != 1 {
		t.Fatalf("replay created a second note: %d", len(page.Items))
	}

	// An event dated before linking is ignored (MX-10); one from the far future is clamped (CORE-N18, SEC-BOT-9).
	old := ev("$old", "@alice:example.org", "ancient")
	old["timestamp"] = time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	s.botDo(key, "POST", "/bot/v1/events", old).JSON(t, &out)
	if out.Result != "ignored" {
		t.Fatalf("pre-link event: %+v", out)
	}
	future := ev("$future", "@alice:example.org", "from the future")
	future["timestamp"] = time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)
	s.botDo(key, "POST", "/bot/v1/events", future).JSON(t, &out)
	if out.Result != "created" {
		t.Fatalf("future event: %+v", out)
	}
	var created time.Time
	if err := s.db.Admin.QueryRow(context.Background(), `select created_at from notes where id = $1`, *out.NoteID).Scan(&created); err != nil || created.After(time.Now().Add(6*time.Minute)) {
		t.Fatalf("created_at not clamped: %v %v", created, err)
	}

	// Unlinking from the app stops ingestion at once (AUTH-B5, SEC-BOT-7) and queues a lifecycle item (BOT-16).
	var ids struct{ Items []struct{ ID string } }
	c.do("GET", "/api/v1/me/identities", nil).JSON(t, &ids)
	if len(ids.Items) != 1 {
		t.Fatalf("identities: %+v", ids)
	}
	if res := c.do("DELETE", "/api/v1/me/identities/"+ids.Items[0].ID, nil); res.Status != 204 {
		t.Fatalf("unlink: %d %s", res.Status, res.Body)
	}
	s.botDo(key, "POST", "/bot/v1/events", ev("$e2", "@alice:example.org", "after unlink")).JSON(t, &out)
	if out.Result != "rejected" {
		t.Fatalf("ingest after unlink: %+v", out)
	}
	var lifecycle int
	_ = s.db.Admin.QueryRow(context.Background(), `select count(*) from bot_outbox where kind = 'lifecycle' and user_id is null`).Scan(&lifecycle)
	if lifecycle != 1 {
		t.Fatalf("lifecycle items: %d", lifecycle)
	}
}

// Bot credentials are per instance, rotatable and disabled at once (AUTH-B1, SEC-BOT-2, SEC-BOT-8).
// Also demonstrates: BOT-1, AUTH-B2.
func TestBotAPIRejectsOtherCredentials(t *testing.T) {
	s := newStack(t)
	s.makeUser("alice", false)
	c := s.newClient()
	c.login("alice")
	if res := s.botDo("", "POST", "/bot/v1/heartbeat", nil); res.Status != 401 {
		t.Fatalf("no key: %d", res.Status)
	}
	if res := s.botDo("nkb.unknown.secret", "POST", "/bot/v1/heartbeat", nil); res.Status != 401 {
		t.Fatalf("unknown key: %d", res.Status)
	}
	// A user's access token is not a bot key (SEC-BOT-2).
	if res := s.botDo(c.cookies["__Host-nka"], "POST", "/bot/v1/heartbeat", nil); res.Status != 401 {
		t.Fatalf("user token on bot API: %d", res.Status)
	}
	key := s.makeBot("m", "example.org")
	if res := s.botDo(key, "POST", "/bot/v1/heartbeat", nil); res.Status != 204 {
		t.Fatalf("valid key: %d", res.Status)
	}
	// Disabling the credential takes effect on the very next request (SEC-BOT-8).
	insts, _ := s.svc.Bots.ListInstances(context.Background())
	creds, _ := s.svc.Bots.ListCredentials(context.Background(), insts[0].ID)
	if err := s.svc.Bots.DisableCredential(context.Background(), storeActor(), insts[0].ID, creds[0].ID); err != nil {
		t.Fatal(err)
	}
	if res := s.botDo(key, "POST", "/bot/v1/heartbeat", nil); res.Status != 401 {
		t.Fatalf("disabled key: %d", res.Status)
	}
	// And a bot key is not a user session.
	if res := s.newClient().doWith("GET", "/api/v1/me", nil, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) }); res.Status != 401 {
		t.Fatalf("bot key on user API: %d", res.Status)
	}
}

func TestOnePersonCannotSeeAnothersNotes(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	a, b := s.makeUser("alice", false), s.makeUser("bob", false)
	key := s.makeBot("m", "example.org")
	linkChat := func(user uuid.UUID, ext string) {
		insts, _ := s.svc.Bots.ListInstances(ctx)
		pc, err := s.svc.Bots.CreatePairingCode(ctx, user, "", &insts[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		s.botDo(key, "POST", "/bot/v1/commands", map[string]any{"command": "link", "args": pc.Code, "sender": ext, "conversation": "!r:" + ext})
	}
	linkChat(a, "@alice:example.org")
	linkChat(b, "@bob:example.org")
	var out struct {
		NoteID *string `json:"note_id"`
	}
	s.botDo(key, "POST", "/bot/v1/events", map[string]any{"event_id": "$a1", "kind": "message_created", "sender": "@alice:example.org",
		"conversation": "!r", "message_id": "$a1", "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"parts": []map[string]any{{"type": "text", "text": "alice secret"}}}).JSON(t, &out)
	if out.NoteID == nil {
		t.Fatal("no note")
	}

	bc := s.newClient()
	bc.login("bob")
	if res := bc.do("GET", "/api/v1/notes/"+*out.NoteID, nil); res.Status != 404 || res.Code() != "not_found" {
		t.Fatalf("bob reading alice's note: %d %s", res.Status, res.Body)
	}
	if res := bc.do("GET", "/api/v1/notes/"+uuid.NewString(), nil); res.Status != 404 {
		t.Fatalf("missing note: %d", res.Status)
	}
	var page notePage
	bc.do("GET", "/api/v1/inbox/notes", nil).JSON(t, &page)
	if len(page.Items) != 0 {
		t.Fatalf("bob's inbox leaks: %+v", page)
	}
	ac := s.newClient()
	ac.login("alice")
	if res := ac.do("GET", "/api/v1/notes/"+*out.NoteID, nil); res.Status != 200 {
		t.Fatalf("alice reading her note: %d", res.Status)
	}
	// Sessions and identities of another user look non-existent as well (SEC-ISO-3).
	var sess struct{ Items []struct{ ID string } }
	ac.do("GET", "/api/v1/me/sessions", nil).JSON(t, &sess)
	if res := bc.do("DELETE", "/api/v1/me/sessions/"+sess.Items[0].ID, nil); res.Status != 404 {
		t.Fatalf("revoking someone else's session: %d", res.Status)
	}
	var ids struct{ Items []struct{ ID string } }
	ac.do("GET", "/api/v1/me/identities", nil).JSON(t, &ids)
	if res := bc.do("DELETE", "/api/v1/me/identities/"+ids.Items[0].ID, nil); res.Status != 404 {
		t.Fatalf("unlinking someone else's identity: %d", res.Status)
	}
}

func TestChangeFeed(t *testing.T) {
	s := newStack(t)
	s.makeUser("alice", false)
	c := s.newClient()
	c.login("alice")
	var start struct{ Cursor string }
	c.do("GET", "/api/v1/changes", nil).JSON(t, &start)

	key := s.makeBot("m", "example.org")
	insts, _ := s.svc.Bots.ListInstances(context.Background())
	me, _ := s.svc.Accounts.Authenticate(context.Background(), c.cookies["__Host-nka"])
	pc, _ := s.svc.Bots.CreatePairingCode(context.Background(), me.UserID, "", &insts[0].ID)
	s.botDo(key, "POST", "/bot/v1/commands", map[string]any{"command": "link", "args": pc.Code, "sender": "@alice:example.org", "conversation": "!r"})
	for i := range 3 {
		id := "$m" + string(rune('a'+i))
		s.botDo(key, "POST", "/bot/v1/events", map[string]any{"event_id": id, "kind": "message_created", "sender": "@alice:example.org",
			"conversation": "!r", "message_id": id, "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"parts": []map[string]any{{"type": "text", "text": id}}})
	}
	var feed struct {
		Items []struct {
			Seq        int64  `json:"seq"`
			EntityType string `json:"entity_type"`
		} `json:"items"`
		Cursor string `json:"cursor"`
	}
	c.do("GET", "/api/v1/changes?cursor="+start.Cursor, nil).JSON(t, &feed)
	if len(feed.Items) != 5 { // identity, the notice about it, and three notes
		t.Fatalf("changes: %+v", feed)
	}
	for i, it := range feed.Items {
		if it.Seq != feed.Items[0].Seq+int64(i) {
			t.Fatalf("change feed has gaps: %+v", feed.Items)
		}
	}
	// Nothing after the returned cursor; a bogus or future cursor is refused.
	var empty struct{ Items []any }
	c.do("GET", "/api/v1/changes?cursor="+feed.Cursor, nil).JSON(t, &empty)
	if len(empty.Items) != 0 {
		t.Fatalf("expected no further changes: %+v", empty)
	}
	if res := c.do("GET", "/api/v1/changes?cursor=!!!", nil); res.Status != 400 {
		t.Fatalf("bad cursor: %d", res.Status)
	}
	if res := c.do("GET", "/api/v1/changes?cursor=OTk5OTk5", nil); res.Status != 410 {
		t.Fatalf("future cursor: %d", res.Status)
	}
}

func TestSessionRevocationClosesEventStream(t *testing.T) {
	s := newStack(t)
	s.makeUser("alice", false)
	c := s.newClient()
	c.login("alice")
	sse := openSSE(t, s, c)
	defer sse.close()
	sse.next(t) // hello
	if res := c.do("POST", "/api/v1/auth/logout", nil); res.Status != 204 {
		t.Fatalf("logout: %d", res.Status)
	}
	// The stream must end promptly (SEC-ISO-5): reading returns EOF.
	done := make(chan struct{})
	go func() {
		for sse.sc.Scan() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("event stream still open after the session was revoked")
	}
}

// ---- server-sent events helper --------------------------------------------------------------

type sseEvent struct{ Name, ID, Data string }

type sseStream struct {
	sc   *bufio.Scanner
	body interface{ Close() error }
}

func openSSE(t *testing.T, s *stack, c *client) *sseStream { return openSSEAt(t, s.user.URL, c) }

func openSSEAt(t *testing.T, base string, c *client) *sseStream {
	return openSSEWithLastID(t, base, c, "")
}

func openSSEWithLastID(t *testing.T, base string, c *client, lastID string) *sseStream {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/v1/events", nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	for k, v := range c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	return &sseStream{sc: bufio.NewScanner(res.Body), body: res.Body}
}

func (s *sseStream) close() { _ = s.body.Close() }

func (s *sseStream) next(t *testing.T) sseEvent {
	t.Helper()
	var ev sseEvent
	got := false
	for s.sc.Scan() {
		line := s.sc.Text()
		switch {
		case line == "":
			if got {
				return ev
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "id: "):
			ev.ID, got = strings.TrimPrefix(line, "id: "), true
		case strings.HasPrefix(line, "event: "):
			ev.Name, got = strings.TrimPrefix(line, "event: "), true
		case strings.HasPrefix(line, "data: "):
			ev.Data, got = strings.TrimPrefix(line, "data: "), true
		}
	}
	t.Fatal("event stream ended")
	return ev
}

func (s *sseStream) until(t *testing.T, name string) sseEvent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ev := s.next(t); ev.Name == name {
			return ev
		}
	}
	t.Fatalf("no %q event within 10s", name)
	return sseEvent{}
}
