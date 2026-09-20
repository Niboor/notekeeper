//go:build integration

package server_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/app"
	"github.com/Niboor/notekeeper/core/internal/realtime"
	"github.com/Niboor/notekeeper/core/internal/server"
	"github.com/Niboor/notekeeper/core/internal/store"
)

// linked creates a user with a linked chat identity and returns a signed-in web client and the bot key.
func (s *stack) linked(name, external string) (*client, string) {
	s.t.Helper()
	ctx := context.Background()
	id := s.makeUser(name, false)
	key := s.bots("m-"+name, "example.org")
	insts, _ := s.svc.Bots.ListInstances(ctx)
	var inst uuid.UUID
	for _, i := range insts {
		if i.Name == "m-"+name {
			inst = i.ID
		}
	}
	pc, err := s.svc.Bots.CreatePairingCode(ctx, id, "", &inst)
	if err != nil {
		s.t.Fatal(err)
	}
	if res := s.botDo(key, "POST", "/bot/v1/commands", map[string]any{"command": "link", "args": pc.Code, "sender": external, "conversation": "!r:" + name}); res.Status != 200 {
		s.t.Fatalf("link: %d %s", res.Status, res.Body)
	}
	c := s.newClient()
	c.login(name)
	return c, key
}

func (s *stack) bots(name, domain string) string { return s.makeBot(name, domain) }

func (s *stack) event(key, sender, id, text string, ts time.Time) response {
	return s.botDo(key, "POST", "/bot/v1/events", map[string]any{"event_id": id, "kind": "message_created", "sender": sender,
		"conversation": "!r", "message_id": id, "timestamp": ts.UTC().Format(time.RFC3339Nano),
		"parts": []map[string]any{{"type": "text", "text": text}}})
}

// The Inbox is ordered by the platform timestamp, not by arrival (CORE-N2, CORE-N18, design decision D1),
// and pages through it with an opaque cursor.
func TestInboxOrderAndPagination(t *testing.T) {
	s := newStack(t)
	c, key := s.linked("alice", "@alice:example.org")
	base := time.Now().Add(time.Second)
	// Arrival order differs from platform order: a backlog delivered after the bot was down.
	for i, off := range []int{3, 1, 4, 0, 2} {
		if res := s.event(key, "@alice:example.org", fmt.Sprintf("$e%d", i), fmt.Sprintf("note %d", off), base.Add(time.Duration(off)*time.Second)); res.Status != 200 {
			t.Fatalf("event: %d %s", res.Status, res.Body)
		}
	}
	var got []string
	cursor := ""
	pages := 0
	for {
		var page notePage
		path := "/api/v1/inbox/notes?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		c.do("GET", path, nil).JSON(t, &page)
		pages++
		for _, n := range page.Items {
			got = append(got, *n.Parts[0].Text)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if strings.Join(got, ",") != "note 4,note 3,note 2,note 1,note 0" || pages != 3 {
		t.Fatalf("order %v over %d pages", got, pages)
	}
	if res := c.do("GET", "/api/v1/inbox/notes?cursor=garbage", nil); res.Status != 400 {
		t.Fatalf("bad cursor: %d", res.Status)
	}
	// A limit above the maximum (200) is capped, not honoured (SEC-API-3).
	var capped notePage
	c.do("GET", "/api/v1/inbox/notes?limit=500", nil).JSON(t, &capped)
	if len(capped.Items) != 5 {
		t.Fatalf("capped page: %d items", len(capped.Items))
	}
}

// Two identical deliveries at the same moment (a bot restarted mid-batch, or two replicas by
// mistake) create one note (BOT-7, BOT-B4).
func TestConcurrentDuplicateDeliveryCreatesOneNote(t *testing.T) {
	s := newStack(t)
	c, key := s.linked("alice", "@alice:example.org")
	ts := time.Now().Add(time.Second)
	var wg sync.WaitGroup
	results := make(chan string, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := s.event(key, "@alice:example.org", "$same", "only once", ts)
			var out struct {
				NoteID string `json:"note_id"`
			}
			res.JSON(t, &out)
			results <- out.NoteID
		}()
	}
	wg.Wait()
	close(results)
	ids := map[string]bool{}
	for id := range results {
		ids[id] = true
	}
	if len(ids) != 1 {
		t.Fatalf("all deliveries must get the same answer, got %v", ids)
	}
	var page notePage
	c.do("GET", "/api/v1/inbox/notes", nil).JSON(t, &page)
	if len(page.Items) != 1 {
		t.Fatalf("%d notes for one event", len(page.Items))
	}
}

// A change made through one Core replica reaches an event stream held by another (CORE-S2).
func TestChangesReachStreamsOnOtherReplicas(t *testing.T) {
	s := newStack(t)
	c, key := s.linked("alice", "@alice:example.org")

	// A second replica: same database, its own hub and listeners.
	hub2 := realtime.NewHub(s.db.AppURL, discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub2.Run(ctx)
	routers2, err := server.NewRouters(server.Deps{Config: s.cfg, Log: discardLog(), Store: s.st, Accounts: s.svc.Accounts, Bots: s.svc.Bots,
		Notes: s.svc.Notes, Board: s.svc.Board, Blobs: s.svc.Blobs, Ingest: s.svc.Ingest, Outbox: s.svc.Outbox, Shares: s.svc.Shares, Reminders: s.svc.Reminders, Export: s.svc.Export, Hub: hub2})
	if err != nil {
		t.Fatal(err)
	}
	replica2 := httptestServer(routers2.User)
	defer replica2.Close()
	defer hub2.Shutdown()

	stream := openSSEAt(t, replica2.URL, c)
	defer stream.close()
	stream.next(t) // hello
	if res := s.event(key, "@alice:example.org", "$x1", "via replica one", time.Now().Add(time.Second)); res.Status != 200 {
		t.Fatalf("event: %d", res.Status)
	}
	ev := stream.until(t, "change")
	if !strings.Contains(ev.Data, `"note"`) {
		t.Fatalf("event on the other replica: %+v", ev)
	}
}

// A dropped connection resumes from Last-Event-ID without missing or repeating changes (CORE-S1, WEB-11).
func TestStreamResumesFromLastEventID(t *testing.T) {
	s := newStack(t)
	c, key := s.linked("alice", "@alice:example.org")
	for i := range 3 {
		s.event(key, "@alice:example.org", fmt.Sprintf("$r%d", i), fmt.Sprintf("n%d", i), time.Now().Add(time.Second))
	}
	var feed struct {
		Items []struct{ Seq int64 } `json:"items"`
	}
	c.do("GET", "/api/v1/changes?cursor=MA", nil).JSON(t, &feed) // cursor "0"
	if len(feed.Items) < 4 {
		t.Fatalf("feed: %+v", feed)
	}
	from := feed.Items[1].Seq
	stream := openSSEWithLastID(t, s.user.URL, c, fmt.Sprint(from))
	defer stream.close()
	var seqs []int64
	for len(seqs) < len(feed.Items)-2 {
		ev := stream.next(t)
		if ev.Name != "change" {
			t.Fatalf("unexpected %+v", ev)
		}
		var n int64
		fmt.Sscan(ev.ID, &n)
		seqs = append(seqs, n)
	}
	for i, n := range seqs {
		if n != from+int64(i)+1 {
			t.Fatalf("resumed stream skipped or repeated changes: %v after %d", seqs, from)
		}
	}
}

// Every note part records which bot instance and chat identity created it (AUTH-B7), and the audit
// log holds the security events without content (AUTH-B8, SEC-AUD-1, SEC-AUD-2).
func TestAttributionAndAudit(t *testing.T) {
	s := newStack(t)
	_, key := s.linked("alice", "@alice:example.org")
	res := s.event(key, "@alice:example.org", "$a1", "secret shopping list", time.Now().Add(time.Second))
	var out struct {
		NoteID string `json:"note_id"`
	}
	res.JSON(t, &out)
	var inst, ident *uuid.UUID
	var conv, msg *string
	if err := s.db.Admin.QueryRow(context.Background(),
		`select source_bot_instance_id, source_identity_id, source_conversation_id, source_message_id from note_parts where note_id = $1`, out.NoteID).
		Scan(&inst, &ident, &conv, &msg); err != nil {
		t.Fatal(err)
	}
	if inst == nil || ident == nil || conv == nil || *msg != "$a1" {
		t.Fatalf("attribution: %v %v %v %v", inst, ident, conv, msg)
	}
	rows, err := s.db.Admin.Query(context.Background(), `select action, detail::text from audit_log`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	actions := map[string]bool{}
	for rows.Next() {
		var a, d string
		_ = rows.Scan(&a, &d)
		actions[a] = true
		if strings.Contains(d, "secret shopping") || strings.Contains(d, key) {
			t.Fatalf("audit detail holds content or a secret: %s", d)
		}
	}
	for _, want := range []string{"bot.instance_created", "bot.credential_created", "identity.linked", "user.activated", "login.succeeded"} {
		if !actions[want] {
			t.Errorf("audit log lacks %q (have %v)", want, actions)
		}
	}
}

// Chat commands: link rules and the other commands (AUTH-B3, AUTH-B4, MX-8).
func TestChatCommands(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	alice, bob := s.makeUser("alice", false), s.makeUser("bob", false)
	key := s.makeBot("m", "example.org")
	insts, _ := s.svc.Bots.ListInstances(ctx)
	inst := insts[0].ID
	send := func(sender, command, args string) (bool, string) {
		var r struct {
			Ok       bool `json:"ok"`
			Feedback struct {
				ReplyText *string `json:"reply_text"`
			} `json:"feedback"`
		}
		s.botDo(key, "POST", "/bot/v1/commands", map[string]any{"command": command, "args": args, "sender": sender, "conversation": "!r:x"}).JSON(t, &r)
		text := ""
		if r.Feedback.ReplyText != nil {
			text = *r.Feedback.ReplyText
		}
		return r.Ok, text
	}
	code := func(user uuid.UUID) string {
		pc, err := s.svc.Bots.CreatePairingCode(ctx, user, "", &inst)
		if err != nil {
			t.Fatal(err)
		}
		return pc.Code
	}

	// help works for anyone, including unlinked senders.
	if ok, text := send("@stranger:example.org", "help", ""); !ok || !strings.Contains(text, "!link") {
		t.Fatalf("help: %v %q", ok, text)
	}
	// Other commands from an unlinked sender behave like any message from one; nothing happens.
	if ok, _ := send("@stranger:example.org", "unlink", ""); ok {
		t.Fatal("unlink for an unlinked sender must not succeed")
	}
	// A code without arguments explains the syntax.
	if ok, text := send("@alice:example.org", "link", ""); ok || !strings.Contains(text, "!link") {
		t.Fatalf("link without code: %v %q", ok, text)
	}
	if ok, _ := send("@alice:example.org", "link", code(alice)); !ok {
		t.Fatal("link failed")
	}
	// The identity is linked to one user only: bob's code cannot claim alice's identity (AUTH-B4, SEC-BOT-6).
	if ok, text := send("@alice:example.org", "link", code(bob)); ok || !strings.Contains(text, "already linked") {
		t.Fatalf("second link of the same identity: %v %q", ok, text)
	}
	// A code bound to another bot instance does not work here.
	other := s.makeBot("other", "example.org")
	_ = other
	insts, _ = s.svc.Bots.ListInstances(ctx)
	var otherID uuid.UUID
	for _, i := range insts {
		if i.Name == "other" {
			otherID = i.ID
		}
	}
	pc, _ := s.svc.Bots.CreatePairingCode(ctx, bob, "", &otherID)
	if ok, _ := send("@bob:example.org", "link", pc.Code); ok {
		t.Fatal("a code for another bot instance was accepted")
	}
	// Unknown commands from a linked sender get a helpful answer.
	if ok, text := send("@alice:example.org", "frobnicate", ""); ok || !strings.Contains(text, "!help") {
		t.Fatalf("unknown command: %v %q", ok, text)
	}
	// Unlink through chat removes the identity at once.
	if ok, _ := send("@alice:example.org", "unlink", ""); !ok {
		t.Fatal("unlink failed")
	}
	var n int
	_ = s.db.Admin.QueryRow(ctx, `select count(*) from external_identities where user_id = $1`, alice).Scan(&n)
	if n != 0 {
		t.Fatal("identity still linked after !unlink")
	}
	// At most five active pairing codes per user.
	carol := s.makeUser("carol", false)
	for range 5 {
		code(carol)
	}
	if _, err := s.svc.Bots.CreatePairingCode(ctx, carol, "", &inst); err == nil {
		t.Fatal("more than five active pairing codes")
	}
}

// Wrong pairing codes are throttled per chat identity (SEC-BOT-4), and codes expire (AUTH-B3).
func TestPairingThrottleAndExpiry(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	alice := s.makeUser("alice", false)
	key := s.makeBot("m", "example.org")
	insts, _ := s.svc.Bots.ListInstances(ctx)
	inst := insts[0].ID
	send := func(args string) string {
		var r struct {
			Feedback struct {
				ReplyText *string `json:"reply_text"`
			} `json:"feedback"`
		}
		s.botDo(key, "POST", "/bot/v1/commands", map[string]any{"command": "link", "args": args, "sender": "@alice:example.org", "conversation": "!r"}).JSON(t, &r)
		if r.Feedback.ReplyText == nil {
			return ""
		}
		return *r.Feedback.ReplyText
	}
	for i := range 4 {
		if !strings.Contains(send(fmt.Sprintf("AAAA-AAA%d", i)), "not valid") {
			t.Fatalf("guess %d", i)
		}
	}
	pc, _ := s.svc.Bots.CreatePairingCode(ctx, alice, "", &inst)
	// Guessing continues to fail; after the free attempts the chat is told to wait, even for a correct code.
	send("BBBB-BBBB")
	if got := send(pc.Code); !strings.Contains(got, "Too many attempts") {
		t.Fatalf("throttled identity got %q", got)
	}

	// Expiry: a code older than ten minutes is refused.
	s2 := newStack(t)
	u := s2.makeUser("alice", false)
	key2 := s2.makeBot("m", "example.org")
	insts2, _ := s2.svc.Bots.ListInstances(ctx)
	code, _ := s2.svc.Bots.CreatePairingCode(ctx, u, "", &insts2[0].ID)
	if _, err := s2.db.Admin.Exec(ctx, `update pairing_codes set expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	var r struct {
		Ok bool `json:"ok"`
	}
	s2.botDo(key2, "POST", "/bot/v1/commands", map[string]any{"command": "link", "args": code.Code, "sender": "@alice:example.org", "conversation": "!r"}).JSON(t, &r)
	if r.Ok {
		t.Fatal("expired pairing code accepted")
	}
}

var _ = app.Services{}
var _ = store.ErrNotFound

// Sign-up is closed and there is no self-service reset: the only operations an anonymous caller
// can invoke are these, and none creates an account or sets a password without a link (AUTH-U3,
// SEC-AUTH-11). Adding an anonymous operation fails this test until someone looks at it.
func TestOnlyExpectedOperationsAreAnonymous(t *testing.T) {
	spec, err := userapiSpec()
	if err != nil {
		t.Fatal(err)
	}
	reqs, err := server.RequirementsFromSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	var anon []string
	for k, r := range reqs {
		if r == server.Anonymous {
			anon = append(anon, k)
		}
	}
	slices.Sort(anon)
	want := []string{"GET /api/v1/version", "POST /api/v1/auth/activate", "POST /api/v1/auth/login", "POST /api/v1/auth/refresh"}
	if !slices.Equal(anon, want) {
		t.Fatalf("anonymous operations changed:\n got  %v\n want %v", anon, want)
	}
}

func TestPendingAndDisabledAccountsCannotAuthenticateOrIngest(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	// A pending account (created, never activated) cannot log in (SEC-AUTH-1).
	if _, err := s.svc.Accounts.CreateUser(ctx, storeActor(), accountsCreate("pending")); err != nil {
		t.Fatal(err)
	}
	if res := s.newClient().do("POST", "/api/v1/auth/login", map[string]any{"username": "pending", "password": password}); res.Status != 401 {
		t.Fatalf("pending login: %d", res.Status)
	}
	// A newer activation link invalidates the older one (SEC-AUTH-4).
	u, _ := s.svc.Accounts.CreateUser(ctx, storeActor(), accountsCreate("dora"))
	first, _ := s.svc.Accounts.IssueActivation(ctx, storeActor(), u.ID)
	second, _ := s.svc.Accounts.IssueActivation(ctx, storeActor(), u.ID)
	if res := s.newClient().do("POST", "/api/v1/auth/activate", map[string]any{"token": first.Token, "password": password}); res.Status != 400 {
		t.Fatalf("superseded link accepted: %d", res.Status)
	}
	if res := s.newClient().do("POST", "/api/v1/auth/activate", map[string]any{"token": second.Token, "password": password}); res.Status != 200 {
		t.Fatalf("newest link: %d", res.Status)
	}

	// A disabled user's chat identity stops ingesting at once (SEC-BOT-13).
	c, key := s.linked("erin", "@erin:example.org")
	_ = c
	var erin uuid.UUID
	_ = s.db.Admin.QueryRow(ctx, `select id from users where username = 'erin'`).Scan(&erin)
	if err := s.svc.Accounts.SetDisabled(ctx, storeActor(), erin, true); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Result string `json:"result"`
		Code   string `json:"code"`
	}
	s.event(key, "@erin:example.org", "$d1", "while disabled", time.Now().Add(time.Second)).JSON(t, &out)
	if out.Result != "rejected" || out.Code != "user_inactive" {
		t.Fatalf("ingest for a disabled user: %+v", out)
	}
	var n int
	_ = s.db.Admin.QueryRow(ctx, `select count(*) from notes where user_id = $1`, erin).Scan(&n)
	if n != 0 {
		t.Fatal("a note was stored for a disabled user")
	}
	// The identity lookup answers link status only (BOT-2, SEC-BOT-3).
	var idr struct {
		Linked bool `json:"linked"`
	}
	s.botDo(key, "GET", "/bot/v1/identities/@erin:example.org", nil).JSON(t, &idr)
	if !idr.Linked {
		t.Fatal("identity lookup")
	}
	s.botDo(key, "GET", "/bot/v1/identities/@nobody:example.org", nil).JSON(t, &idr)
	if idr.Linked {
		t.Fatal("unknown identity reported as linked")
	}
}

// A bot instance acts only for identities linked through it (SEC-BOT-1): a second instance of the
// same type cannot ingest for, or even see, an identity that was linked through the first.
func TestBotInstancesAreScopedToTheirOwnLinks(t *testing.T) {
	s := newStack(t)
	c, keyA := s.linked("alice", "@alice:example.org")
	keyB := s.makeBot("second", "example.org")

	var out struct {
		Result string `json:"result"`
		Code   string `json:"code"`
	}
	s.event(keyB, "@alice:example.org", "$b1", "via the other bot", time.Now().Add(time.Second)).JSON(t, &out)
	if out.Result != "rejected" || out.Code != "identity_unlinked" {
		t.Fatalf("second instance ingested for an identity it does not hold: %+v", out)
	}
	var idr struct {
		Linked bool `json:"linked"`
	}
	s.botDo(keyB, "GET", "/bot/v1/identities/@alice:example.org", nil).JSON(t, &idr)
	if idr.Linked {
		t.Fatal("the identity lookup revealed a link made through another instance")
	}
	if res := s.event(keyA, "@alice:example.org", "$a1", "via the first bot", time.Now().Add(time.Second)); res.Status != 200 {
		t.Fatal(res.Status)
	}
	var page notePage
	c.do("GET", "/api/v1/inbox/notes", nil).JSON(t, &page)
	if len(page.Items) != 1 {
		t.Fatalf("notes: %d", len(page.Items))
	}
}

// A pairing code redeemed by many chats at the same moment links exactly one of them (SEC-BOT-4, SEC-API-7).
func TestPairingCodeIsSingleUseUnderRace(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	alice := s.makeUser("alice", false)
	key := s.makeBot("m", "example.org")
	insts, _ := s.svc.Bots.ListInstances(ctx)
	pc, err := s.svc.Bots.CreatePairingCode(ctx, alice, "", &insts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var linked atomic.Int32
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r struct {
				Ok bool `json:"ok"`
			}
			s.botDo(key, "POST", "/bot/v1/commands", map[string]any{"command": "link", "args": pc.Code,
				"sender": fmt.Sprintf("@user%d:example.org", i), "conversation": "!r"}).JSON(t, &r)
			if r.Ok {
				linked.Add(1)
			}
		}()
	}
	wg.Wait()
	if linked.Load() != 1 {
		t.Fatalf("%d chats linked with one code", linked.Load())
	}
	if n := s.count(`select count(*) from external_identities`); n != 1 {
		t.Fatalf("%d identities exist", n)
	}
}

// A note created through the bot path is indexed for search like any other: the search trigger
// runs inside the ingest transaction with the user context set (CORE-N13).
func TestChatNotesAreSearchable(t *testing.T) {
	s := newStack(t)
	c, key := s.linked("alice", "@alice:example.org")
	if res := s.event(key, "@alice:example.org", "$s1", "Concert tickets for friday", time.Now().Add(time.Second)); res.Status != 200 {
		t.Fatal(res.Status)
	}
	var r struct {
		Items []struct{ Note struct{ ID string } } `json:"items"`
	}
	c.do("GET", "/api/v1/search?q=tickets", nil).JSON(t, &r)
	if len(r.Items) != 1 {
		t.Fatalf("a note that arrived from chat is not searchable: %+v", r)
	}
}
