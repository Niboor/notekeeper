//go:build integration

package e2eetest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/Niboor/notekeeper/bots/matrix/internal/bot"
	"github.com/Niboor/notekeeper/bots/matrix/internal/config"
	"github.com/Niboor/notekeeper/bots/sdk"
)

// fakeCore stands in for Core's bot API: it records what the bot sends and answers as scripted.
// (The whole chain with the real Core runs in the end-to-end tests.)
type fakeCore struct {
	srv *httptest.Server

	mu        sync.Mutex
	events    []map[string]any // accepted, deduplicated by event_id like Core does
	seen      map[string]bool
	commands  []map[string]any
	attempts  int
	unavail   atomic.Bool // true: answer 503 (Core is down)
	reply     map[string]string
	heartbeat atomic.Int32
}

func newFakeCore(t *testing.T) *fakeCore {
	f := &fakeCore{seen: map[string]bool{}, reply: map[string]string{}}
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/bot/v1/version", func(w http.ResponseWriter, _ *http.Request) { respond(w, map[string]string{"version": "test"}) })
	mux.HandleFunc("/bot/v1/heartbeat", func(w http.ResponseWriter, _ *http.Request) { f.heartbeat.Add(1); w.WriteHeader(204) })
	mux.HandleFunc("/bot/v1/events", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.attempts++
		f.mu.Unlock()
		if f.unavail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		id, _ := body["event_id"].(string)
		if !f.seen[id] { // at-least-once delivery, deduplicated on event id (BOT-7)
			f.seen[id] = true
			f.events = append(f.events, body)
		}
		respond(w, map[string]any{"result": "created", "feedback": map[string]any{"react": "ok"}})
	})
	mux.HandleFunc("/bot/v1/commands", func(w http.ResponseWriter, r *http.Request) {
		if f.unavail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.commands = append(f.commands, body)
		text := f.reply[body["command"].(string)]
		f.mu.Unlock()
		fb := map[string]any{}
		if text != "" {
			fb["reply_text"] = text
		}
		respond(w, map[string]any{"ok": true, "feedback": fb})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCore) eventTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		parts, _ := e["parts"].([]any)
		for _, p := range parts {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func (f *fakeCore) waitEvents(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := len(f.events)
		f.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("core did not receive %d events in %v (has %v)", n, timeout, f.eventTexts())
}

// runningBot is a real bot (encryption, PostgreSQL state, sync loop) on its own database.
type runningBot struct {
	t      *testing.T
	b      *bot.Bot
	cancel context.CancelFunc
	done   chan error
}

func botConfig(hs, dbURL, coreURL, user string) config.Config {
	return config.Config{
		Homeserver: hs, User: user, Password: "pw-" + user, PickleKey: []byte("bot-pickle-key-0123456789abcdef!"),
		DatabaseURL: dbURL, InstanceName: "matrix-test", CoreURL: coreURL, BotKey: "nkb.test.secret", HeartbeatEvery: 200 * time.Millisecond,
	}
}

func startBot(t *testing.T, cfg config.Config) *runningBot {
	t.Helper()
	core, err := sdk.NewClient(cfg.CoreURL, cfg.BotKey, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	core.Backoff = func(int) time.Duration { return 100 * time.Millisecond }
	var out io.Writer = io.Discard
	if os.Getenv("NK_TEST_VERBOSE") != "" {
		out = os.Stderr // bot logs never contain message text, so this is safe to enable
	}
	b := bot.New(cfg, slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug})), core)
	ctx, cancel := context.WithCancel(context.Background())
	if err := b.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start bot: %v", err)
	}
	rb := &runningBot{t: t, b: b, cancel: cancel, done: make(chan error, 1)}
	go b.Heartbeat(ctx)
	go func() { rb.done <- b.Sync(ctx) }()
	t.Cleanup(rb.stop)
	deadline := time.Now().Add(30 * time.Second)
	for b.LastSync().IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("bot never completed a sync")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return rb
}

func (r *runningBot) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(20 * time.Second):
		r.t.Error("bot did not stop")
	}
	_ = r.b.Close()
	r.cancel = nil
}

// person is a Matrix user on "Element": an encrypted client that collects what it receives.
func person(t *testing.T, hs string, pg *postgresDB, name string) *actor {
	t.Helper()
	a := &actor{t: t, name: name, homeserver: hs, dbURL: pg.newDatabase(t, name), pickleKey: []byte("person-pickle-key-0123456789abc")}
	a.start(true)
	t.Cleanup(a.stop)
	return a
}

// createDM has alice open an encrypted room with the bot, as Element does; direct sets the is_direct flag.
func createDM(t *testing.T, alice *actor, botUser id.UserID, direct bool) id.RoomID {
	t.Helper()
	req := &mautrix.ReqCreateRoom{Invite: []id.UserID{botUser}, IsDirect: direct, Preset: "trusted_private_chat"}
	req.InitialState = []*event.Event{{Type: event.StateEncryption, StateKey: new(string),
		Content: event.Content{Parsed: &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}}}}
	resp, err := alice.client.CreateRoom(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.RoomID
}

func waitMembership(t *testing.T, c *mautrix.Client, room id.RoomID, user id.UserID, want event.Membership, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var content event.MemberEventContent
		if err := c.StateEvent(context.Background(), room, event.StateMember, user.String(), &content); err == nil && content.Membership == want {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s did not reach membership %s in %s", user, want, room)
}

func registerUser(t *testing.T, hs, name string) {
	t.Helper()
	c, err := mautrix.NewClient(hs, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterDummy(context.Background(), &mautrix.ReqRegister[any]{Username: name, Password: "pw-" + name}); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

// waitReaction waits for a reaction with key under an event.
func waitReaction(t *testing.T, a *actor, room id.RoomID, target id.EventID, key string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		rel, err := a.client.GetRelations(context.Background(), room, target, &mautrix.ReqGetRelations{RelationType: event.RelAnnotation})
		if err == nil {
			for _, e := range rel.Chunk {
				_ = e.Content.ParseRaw(event.EventReaction)
				if r := e.Content.AsReaction(); r != nil && r.RelatesTo.Key == key {
					return
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("no %s reaction appeared", key)
}

// waitNotice waits for a decrypted m.notice containing text.
func waitNotice(t *testing.T, a *actor, text string) *event.Event {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case evt := <-a.messages:
			if c := evt.Content.AsMessage(); c != nil && c.MsgType == event.MsgNotice && strings.Contains(c.Body, text) {
				return evt
			}
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Fatalf("no notice containing %q", text)
	return nil
}

// TestBotForwardsEncryptedMessagesAndShowsFeedback is the bot's main flow (F2): an encrypted
// direct message becomes a Core event with platform identifiers and timestamp, the user sees a
// reaction, commands are forwarded, and Core's reply text comes back as a notice.
func TestBotForwardsEncryptedMessagesAndShowsFeedback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	hs, pg := startSynapse(t), startPostgres(t)
	core := newFakeCore(t)
	core.reply["link"] = "Linked to your account."

	registerUser(t, hs, "notekeeper")
	rb := startBot(t, botConfig(hs, pg.newDatabase(t, "bot_flow"), core.srv.URL, "notekeeper"))
	alice := person(t, hs, pg, "alice_flow")
	room := createDM(t, alice, rb.b.UserID(), true)
	waitMembership(t, alice.client, room, rb.b.UserID(), event.MembershipJoin, 30*time.Second)
	time.Sleep(2 * time.Second) // alice must learn the bot's devices before she can encrypt for it

	sent, err := alice.client.SendText(ctx, room, "buy milk, eggs")
	if err != nil {
		t.Fatal(err)
	}
	core.waitEvents(t, 1, 45*time.Second)
	core.mu.Lock()
	ev := core.events[0]
	core.mu.Unlock()
	if ev["sender"] != alice.client.UserID.String() || ev["conversation"] != room.String() ||
		ev["message_id"] != sent.EventID.String() || ev["event_id"] != sent.EventID.String() || ev["kind"] != "message_created" {
		t.Fatalf("event: %+v", ev)
	}
	if got := core.eventTexts(); len(got) != 1 || got[0] != "buy milk, eggs" {
		t.Fatalf("text: %v", got)
	}
	if ts, _ := ev["timestamp"].(string); ts == "" {
		t.Fatalf("the platform timestamp must be forwarded: %+v", ev)
	}

	// The reaction Core asked for appears under alice's message (BOT-B3, MX-7).
	waitReaction(t, alice, room, sent.EventID, "✅")

	// A command is forwarded (not stored as a note), and Core's reply text arrives as a notice.
	if _, err := alice.client.SendText(ctx, room, "!link ABCD-1234"); err != nil {
		t.Fatal(err)
	}
	notice := waitNotice(t, alice, "Linked to your account.")
	if !notice.Mautrix.WasEncrypted {
		t.Fatal("the bot replied in plaintext in an encrypted room")
	}
	core.mu.Lock()
	cmds := core.commands
	core.mu.Unlock()
	if len(cmds) != 1 || cmds[0]["command"] != "link" || cmds[0]["args"] != "ABCD-1234" || cmds[0]["sender"] != alice.client.UserID.String() {
		t.Fatalf("commands: %+v", cmds)
	}
	if len(core.eventTexts()) != 1 {
		t.Fatalf("a command must not become a note: %v", core.eventTexts())
	}

	// The bot's own notices and reactions never loop back as notes (MX-4).
	time.Sleep(2 * time.Second)
	if len(core.eventTexts()) != 1 {
		t.Fatalf("the bot processed its own output: %v", core.eventTexts())
	}
	if core.heartbeat.Load() == 0 {
		t.Fatal("no heartbeat reached Core")
	}
}

// TestBotOnlyJoinsDirectMessagesOfTwo covers MX-1 and MX-2: invites to group rooms are declined,
// and a direct message that gains a third member is ignored, with one explanation.
func TestBotOnlyJoinsDirectMessagesOfTwo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	hs, pg := startSynapse(t), startPostgres(t)
	core := newFakeCore(t)

	registerUser(t, hs, "notekeeper2")
	rb := startBot(t, botConfig(hs, pg.newDatabase(t, "bot_rooms"), core.srv.URL, "notekeeper2"))
	alice := person(t, hs, pg, "alice_rooms")
	carol := person(t, hs, pg, "carol_rooms")

	// An invite to a room that is not a direct message is declined.
	group := createDM(t, alice, rb.b.UserID(), false)
	waitMembership(t, alice.client, group, rb.b.UserID(), event.MembershipLeave, 30*time.Second)

	// A real direct message works ...
	room := createDM(t, alice, rb.b.UserID(), true)
	waitMembership(t, alice.client, room, rb.b.UserID(), event.MembershipJoin, 30*time.Second)
	time.Sleep(2 * time.Second)
	if _, err := alice.client.SendText(ctx, room, "first"); err != nil {
		t.Fatal(err)
	}
	core.waitEvents(t, 1, 45*time.Second)

	// ... until a third person joins: from then on the room is ignored and explained once.
	if _, err := alice.client.InviteUser(ctx, room, &mautrix.ReqInviteUser{UserID: carol.client.UserID}); err != nil {
		t.Fatal(err)
	}
	waitMembership(t, alice.client, room, carol.client.UserID, event.MembershipInvite, 30*time.Second)
	if _, err := carol.client.JoinRoomByID(ctx, room); err != nil {
		t.Fatal(err)
	}
	waitNotice(t, alice, "more than two members")
	time.Sleep(2 * time.Second) // let the device lists settle
	if _, err := alice.client.SendText(ctx, room, "second"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)
	if got := core.eventTexts(); len(got) != 1 || got[0] != "first" {
		t.Fatalf("a room with three members must be ignored: %v", got)
	}
}

// TestCoreOutageStallsInsteadOfLosingMessages covers NFR-R1 / BOT-B2 with the real bot: while
// Core is down the bot keeps retrying, and a bot killed during the outage replays the message
// after restart (deduplicated by Core on the event id).
func TestCoreOutageStallsInsteadOfLosingMessages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	hs, pg := startSynapse(t), startPostgres(t)
	core := newFakeCore(t)

	registerUser(t, hs, "notekeeper3")
	botDB := pg.newDatabase(t, "bot_outage")
	rb := startBot(t, botConfig(hs, botDB, core.srv.URL, "notekeeper3"))
	alice := person(t, hs, pg, "alice_outage")
	room := createDM(t, alice, rb.b.UserID(), true)
	waitMembership(t, alice.client, room, rb.b.UserID(), event.MembershipJoin, 30*time.Second)
	time.Sleep(2 * time.Second)

	// 1. Core is down while a message arrives: the bot retries; when Core returns it gets the message.
	core.unavail.Store(true)
	if _, err := alice.client.SendText(ctx, room, "during outage"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	core.mu.Lock()
	tried := core.attempts
	core.mu.Unlock()
	if tried < 2 {
		t.Fatalf("the bot should keep retrying while Core is down (attempts=%d)", tried)
	}
	core.unavail.Store(false)
	core.waitEvents(t, 1, 30*time.Second)

	// 2. The bot dies while Core is down and a message is in flight; after restart it must not be lost.
	core.unavail.Store(true)
	if _, err := alice.client.SendText(ctx, room, "killed mid-flight"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second) // the bot is now stuck retrying inside the handler
	rb.stop()
	core.unavail.Store(false)
	startBot(t, botConfig(hs, botDB, core.srv.URL, "notekeeper3"))
	core.waitEvents(t, 2, 60*time.Second)
	got := core.eventTexts()
	if len(got) != 2 || got[0] != "during outage" || got[1] != "killed mid-flight" {
		t.Fatalf("messages after the outage: %v", got)
	}
}
