//go:build integration

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// chatter is a signed-in user whose chat identity is linked to a bot, with helpers that speak the
// bot API the way a bot does (M3: grouping, edits, deletes, attachments).
type chatter struct {
	*appUser
	key, ext, conv string
	base           time.Time
	seq            int
}

type eventOut struct {
	Result string  `json:"result"`
	NoteID *string `json:"note_id"`
	Code   *string `json:"code"`

	Feedback struct {
		React     *string `json:"react"`
		ReplyText *string `json:"reply_text"`
	} `json:"feedback"`
}

// chatter links a new user's chat to the given bot key. Every event timestamp is relative to base,
// which is after the link, so nothing counts as history from before linking.
func (s *stack) chatter(name, key string) *chatter {
	s.t.Helper()
	u := s.appUser(name)
	ext := "@" + name + ":example.org"
	insts, err := s.svc.Bots.ListInstances(context.Background())
	if err != nil || len(insts) == 0 {
		s.t.Fatal("no bot instance", err)
	}
	var uid uuid.UUID
	if err := s.db.Admin.QueryRow(context.Background(), `select id from users where username = $1`, name).Scan(&uid); err != nil {
		s.t.Fatal(err)
	}
	pc, err := s.svc.Bots.CreatePairingCode(context.Background(), uid, "", &insts[0].ID)
	if err != nil {
		s.t.Fatal(err)
	}
	res := s.botDo(key, "POST", "/bot/v1/commands", map[string]any{"command": "link", "args": pc.Code, "sender": ext, "conversation": "!room-" + name})
	if res.Status != 200 {
		s.t.Fatalf("link: %d %s", res.Status, res.Body)
	}
	// Linking a chat and signing in produced notices; tests about other things start without them.
	if _, err := s.db.Admin.Exec(context.Background(), `delete from bot_outbox where kind = 'notice'; delete from notifications where kind = 'security'`); err != nil {
		s.t.Fatal(err)
	}
	// Test timestamps run hours ahead of the real clock; Core's clamp on far-future timestamps
	// (CORE-N18, tested separately) must not squash them.
	s.svc.Ingest.Now = func() time.Time { return time.Now().Add(1000 * time.Hour) }
	return &chatter{appUser: u, key: key, ext: ext, conv: "!room-" + name, base: time.Now()}
}

func text(t string) map[string]any { return map[string]any{"type": "text", "text": t} }

func (ch *chatter) event(kind, msg string, offset time.Duration, rel map[string]any, parts ...map[string]any) eventOut {
	ch.t.Helper()
	ch.seq++
	ev := map[string]any{"event_id": fmt.Sprintf("$ev-%s-%d", ch.ext, ch.seq), "kind": kind, "sender": ch.ext, "conversation": ch.conv, "message_id": msg,
		"timestamp": ch.base.Add(offset).UTC().Format(time.RFC3339Nano), "parts": parts}
	if rel != nil {
		ev["relates_to"] = rel
	}
	return ch.post2(ev)
}

func (ch *chatter) post2(ev map[string]any) eventOut {
	ch.t.Helper()
	res := ch.s.botDo(ch.key, "POST", "/bot/v1/events", ev)
	if res.Status != 200 {
		ch.t.Fatalf("event: %d %s", res.Status, res.Body)
	}
	var out eventOut
	res.JSON(ch.t, &out)
	return out
}

func (ch *chatter) send(msg string, offset time.Duration, parts ...map[string]any) eventOut {
	ch.t.Helper()
	return ch.event("message_created", msg, offset, nil, parts...)
}

func (ch *chatter) edit(msg string, offset time.Duration, newText string) eventOut {
	ch.t.Helper()
	return ch.event("message_edited", msg, offset, nil, text(newText))
}

func (ch *chatter) remove(msg string, offset time.Duration) eventOut {
	ch.t.Helper()
	return ch.event("message_deleted", msg, offset, nil)
}

// botUpload sends a file the way a bot does and returns the response.
func (ch *chatter) botUpload(id, name string, data []byte) response {
	ch.t.Helper()
	req, _ := http.NewRequest("PUT", ch.s.bot.URL+"/bot/v1/uploads/"+id, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+ch.key)
	req.Header.Set("X-External-User", ch.ext)
	req.Header.Set("X-Filename", url.PathEscape(name))
	req.Header.Set("X-Media-Type", "image/png")
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		ch.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	return response{Status: res.StatusCode, Header: res.Header, Body: buf.Bytes()}
}

// photo uploads a file and returns the attachment part that refers to it.
func (ch *chatter) photo(name string) map[string]any {
	ch.t.Helper()
	id := uuid.NewString()
	if res := ch.botUpload(id, name, randomBytes(2000)); res.Status != 201 {
		ch.t.Fatalf("bot upload: %d %s", res.Status, res.Body)
	}
	return map[string]any{"type": "attachment", "upload_id": id, "filename": name, "media_type": "image/png", "size": 2000}
}

type partView struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Text         *string `json:"text"`
	AttachReason string  `json:"attach_reason"`
	Attachment   *struct {
		ID string `json:"id"`
	} `json:"attachment"`
	Failed *struct {
		Reason string `json:"reason"`
	} `json:"failed"`
}

type noteView struct {
	ID    string     `json:"id"`
	State string     `json:"state"`
	Parts []partView `json:"parts"`
}

func (ch *chatter) get(id string) noteView {
	ch.t.Helper()
	var n noteView
	ch.appUser.get("/api/v1/notes/"+id, &n)
	return n
}

func (ch *chatter) texts(n noteView) []string {
	var out []string
	for _, p := range n.Parts {
		if p.Text != nil {
			out = append(out, *p.Text)
		}
	}
	return out
}

func (ch *chatter) notes() int { return ch.s.count(`select count(*) from notes`) }

// Chat messages become one note each; only media joins its neighbour, and a caption joins the
// media that came before it (GRP-2, GRP-3, GRP-4, GRP-5, GRP-8; F3, F4).
func TestGroupingByTimeAndContent(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)

	// Two texts a second apart stay two notes: timing alone never merges text (GRP-4).
	a := ch.send("$t1", 0, text("first"))
	b := ch.send("$t2", time.Second, text("second"))
	if a.Result != "created" || b.Result != "created" || *a.NoteID == *b.NoteID {
		t.Fatalf("two texts: %+v %+v", a, b)
	}

	// A photo burst becomes one note (GRP-3), and a caption sent afterwards joins it (GRP-2).
	p1 := ch.send("$p1", 5*time.Minute+10*time.Second, ch.photo("a.png"))
	p2 := ch.send("$p2", 5*time.Minute+12*time.Second, ch.photo("b.png"))
	p3 := ch.send("$p3", 5*time.Minute+14*time.Second, ch.photo("c.png"))
	c := ch.send("$c", 5*time.Minute+20*time.Second, text("holiday"))
	if p2.Result != "appended" || p3.Result != "appended" || c.Result != "appended" || *p2.NoteID != *p1.NoteID || *c.NoteID != *p1.NoteID {
		t.Fatalf("photo burst: %+v %+v %+v %+v", p1, p2, p3, c)
	}
	n := ch.get(*p1.NoteID)
	if len(n.Parts) != 4 || n.Parts[0].Kind != "attachment" || n.Parts[3].Kind != "text" || n.Parts[0].AttachReason != "first" ||
		n.Parts[1].AttachReason != "media-adjacency" || n.Parts[3].AttachReason != "media-adjacency" {
		t.Fatalf("burst note: %+v", n.Parts)
	}
	// Now the note has a text body, so another text starts a new note, but a further photo still joins (GRP-3, GRP-4).
	d := ch.send("$d", 5*time.Minute+22*time.Second, text("second caption"))
	e := ch.send("$e", 5*time.Minute+24*time.Second, ch.photo("d.png"))
	if d.Result != "created" || *d.NoteID == *p1.NoteID || e.Result != "appended" || *e.NoteID != *d.NoteID {
		t.Fatalf("after caption: %+v %+v", d, e)
	}

	// The window is measured from the note's last part, on platform time (GRP-5): a photo two
	// minutes later starts a new note, and so does one whose timestamp is earlier than the note.
	f := ch.send("$f", 5*time.Minute+24*time.Second+2*time.Minute, ch.photo("e.png"))
	if f.Result != "created" {
		t.Fatalf("outside the window: %+v", f)
	}
	if got := ch.notes(); got != 5 {
		t.Fatalf("notes: %d, want 5", got)
	}

	// A user who sets a longer window gets it (GRP-5).
	if r := ch.c.do("PATCH", "/api/v1/me", map[string]any{"settings": map[string]any{"grouping_window_seconds": 600}}); r.Status != 200 {
		t.Fatalf("settings: %d %s", r.Status, r.Body)
	}
	g := ch.send("$g", 5*time.Minute+24*time.Second+2*time.Minute+5*time.Minute, ch.photo("f.png"))
	if g.Result != "appended" || *g.NoteID != *f.NoteID {
		t.Fatalf("longer window: %+v", g)
	}
}

// A different sender or conversation never joins someone else's note (GRP-2).
func TestGroupingStaysWithinTheSenderAndConversation(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	alice, bob := s.chatter("alice", key), s.chatter("bob", key)
	a := alice.send("$p1", 0, alice.photo("a.png"))
	b := bob.send("$p1", time.Second, bob.photo("b.png"))
	if b.Result != "created" || *a.NoteID == *b.NoteID {
		t.Fatalf("two people: %+v %+v", a, b)
	}
	// The same message id in two users' chats is two different messages.
	if s.count(`select count(*) from note_parts where source_message_id = '$p1'`) != 2 {
		t.Fatal("both messages must be stored")
	}
	// Another conversation of the same sender does not join either.
	ev := map[string]any{"event_id": "$other", "kind": "message_created", "sender": alice.ext, "conversation": "!another", "message_id": "$o1",
		"timestamp": alice.base.Add(2 * time.Second).UTC().Format(time.RFC3339Nano), "parts": []map[string]any{alice.photo("z.png")}}
	if out := alice.post2(ev); out.Result != "created" {
		t.Fatalf("other conversation: %+v", out)
	}
}

// Replies and threads join the note they refer to, whenever they arrive; a reply to a note in the
// Trash starts a new note that remembers the relation (GRP-1, GRP-6, GRP-7, BOT-5).
func TestRepliesAndThreads(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	root := ch.send("$root", 0, text("plan the trip"))
	ch.send("$other", 30*time.Minute, text("unrelated"))

	r := ch.event("message_created", "$r1", time.Hour, map[string]any{"reply_to": "$root"}, text("also book the train"))
	if r.Result != "appended" || *r.NoteID != *root.NoteID {
		t.Fatalf("reply: %+v", r)
	}
	th := ch.event("message_created", "$th1", 2*time.Hour, map[string]any{"thread": "$root"}, text("and the hotel"))
	if th.Result != "appended" || *th.NoteID != *root.NoteID {
		t.Fatalf("thread: %+v", th)
	}
	n := ch.get(*root.NoteID)
	if len(n.Parts) != 3 || n.Parts[1].AttachReason != "reply" || n.Parts[2].AttachReason != "thread" {
		t.Fatalf("parts: %+v", n.Parts)
	}
	// A reply to a message Core has never seen is just a message.
	if u := ch.event("message_created", "$r2", 3*time.Hour, map[string]any{"reply_to": "$unknown"}, text("orphan reply")); u.Result != "created" {
		t.Fatalf("unknown reply target: %+v", u)
	}

	// Dismiss the note; a reply now creates a new note that records what it answered (GRP-7).
	ch.post("/api/v1/notes/"+*root.NoteID+"/dismiss", nil, 200, nil)
	d := ch.event("message_created", "$r3", 4*time.Hour, map[string]any{"reply_to": "$root"}, text("still relevant"))
	if d.Result != "created" || *d.NoteID == *root.NoteID {
		t.Fatalf("reply to dismissed: %+v", d)
	}
	if ch.get(*root.NoteID).State != "deleted" {
		t.Fatal("the dismissed note must stay in the Trash")
	}
	if ch.s.count(`select count(*) from note_parts where note_id = $1 and related_part_id is not null`, *d.NoteID) != 1 {
		t.Fatal("the new note must remember which part it answered")
	}
}

// Chat edits update the note; the latest edit by the time it was made wins over the latest to
// arrive; edits reach notes in the Trash; foreign or unknown messages are ignored (EDT-1, EDT-2, EDT-3, EDT-5, EDT-6, EDT-7).
func TestChatEdits(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	a := ch.send("$m", 0, text("buy milk"))

	if u := ch.edit("$m", 10*time.Second, "buy oat milk"); u.Result != "updated" || *u.NoteID != *a.NoteID {
		t.Fatalf("edit: %+v", u)
	}
	if got := ch.texts(ch.get(*a.NoteID)); len(got) != 1 || got[0] != "buy oat milk" {
		t.Fatalf("after edit: %v", got)
	}
	// An older edit that arrives late is kept in the history but does not overwrite (EDT-5).
	ch.edit("$m", 5*time.Second, "buy soy milk")
	if got := ch.texts(ch.get(*a.NoteID)); got[0] != "buy oat milk" {
		t.Fatalf("a late old edit won: %v", got)
	}
	if ch.s.count(`select count(*) from note_part_versions where applied = false`) != 1 || ch.s.count(`select count(*) from note_part_versions`) != 3 {
		t.Fatalf("history: %d versions", ch.s.count(`select count(*) from note_part_versions`))
	}

	// An edit in the app, then a chat edit made before it: the app edit stays (EDT-2).
	var n noteJSON
	ch.get2(*a.NoteID, &n)
	ch.c.do("PATCH", "/api/v1/notes/"+n.ID+"/parts/"+n.Parts[0].ID, map[string]any{"text": "buy oat milk and bread"})
	ch.edit("$m", time.Millisecond, "chat edit made before the app edit")
	if got := ch.texts(ch.get(*a.NoteID)); got[0] != "buy oat milk and bread" {
		t.Fatalf("the app edit was overwritten by an older chat edit: %v", got)
	}
	// A chat edit made after it wins.
	ch.edit("$m", 2*time.Minute, "final")
	if got := ch.texts(ch.get(*a.NoteID)); got[0] != "final" {
		t.Fatalf("a newer chat edit was ignored: %v", got)
	}

	// The note may be anywhere, the Trash included; the edit reaches it and does not restore it (EDT-7).
	ch.post("/api/v1/notes/"+*a.NoteID+"/dismiss", nil, 200, nil)
	ch.edit("$m", 3*time.Minute, "edited in the trash")
	n2 := ch.get(*a.NoteID)
	if n2.State != "deleted" || ch.texts(n2)[0] != "edited in the trash" {
		t.Fatalf("edit in trash: %+v", n2)
	}

	// Unknown messages are ignored without error (EDT-6).
	if u := ch.edit("$nope", 4*time.Minute, "x"); u.Result != "ignored" {
		t.Fatalf("unknown edit: %+v", u)
	}

	// Another user can neither see nor change this message, even with the same ids (SEC-BOT-1).
	bob := s.chatter("bob", key)
	if u := bob.edit("$m", time.Second, "bob was here"); u.Result != "ignored" {
		t.Fatalf("foreign edit: %+v", u)
	}
	if got := ch.texts(ch.get(*a.NoteID)); got[0] != "edited in the trash" {
		t.Fatalf("foreign edit changed the note: %v", got)
	}
}

func (ch *chatter) get2(id string, into any) { ch.appUser.get("/api/v1/notes/"+id, into) }

// A deleted chat message removes what it added; if that was everything, the note goes to the Trash
// with its content, never silently disappears; deletes of unknown messages are ignored (EDT-4, BOT-9, decision 51).
func TestChatDeletes(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)

	// A caption plus a photo: deleting the caption removes only the caption.
	p := ch.send("$p", 0, ch.photo("a.png"))
	ch.send("$cap", time.Second, text("caption"))
	if u := ch.remove("$cap", 2*time.Second); u.Result != "removed" {
		t.Fatalf("delete: %+v", u)
	}
	n := ch.get(*p.NoteID)
	if n.State != "active" || len(n.Parts) != 1 || n.Parts[0].Kind != "attachment" {
		t.Fatalf("after deleting the caption: %+v", n)
	}
	// Deleting the message that made the last part sends the note to the Trash with its content (EDT-4).
	if u := ch.remove("$p", 3*time.Second); u.Result != "removed" {
		t.Fatalf("delete last: %+v", u)
	}
	n = ch.get(*p.NoteID)
	if n.State != "deleted" || len(n.Parts) != 1 {
		t.Fatalf("the note must be in the Trash with its part: %+v", n)
	}
	// Deleting it again, or a message never seen, is ignored.
	if u := ch.remove("$p", 4*time.Second); u.Result != "removed" {
		t.Fatalf("repeat delete: %+v", u)
	}
	if u := ch.remove("$never", 5*time.Second); u.Result != "ignored" {
		t.Fatalf("unknown delete: %+v", u)
	}

	// A text note whose message is deleted goes to the Trash and can be restored.
	tn := ch.send("$t", 10*time.Second, text("oops"))
	ch.remove("$t", 11*time.Second)
	if ch.get(*tn.NoteID).State != "deleted" {
		t.Fatal("deleted chat message must dismiss its note")
	}
	ch.post("/api/v1/notes/"+*tn.NoteID+"/restore", nil, 200, nil)
	if got := ch.texts(ch.get(*tn.NoteID)); len(got) != 1 || got[0] != "oops" {
		t.Fatalf("restored: %v", got)
	}
}

// A message whose upload is gone still lands, with a visible failed attachment (CORE-A9, BOT-11).
func TestMissingUploadKeepsTheMessage(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	gone := map[string]any{"type": "attachment", "upload_id": uuid.NewString(), "filename": "lost.png", "size": 10}
	failed := map[string]any{"type": "attachment_failed", "filename": "huge.mov", "reason": "too_large", "size": 900_000_000}
	out := ch.send("$m", 0, text("look at this"), gone, failed)
	if out.Result != "created" {
		t.Fatalf("%+v", out)
	}
	// The person is told which files were not kept and why, and that the text is safe (CORE-A9, MX-7).
	if out.Feedback.React == nil || *out.Feedback.React == "ok" || out.Feedback.ReplyText == nil ||
		!strings.Contains(*out.Feedback.ReplyText, "lost.png") || !strings.Contains(*out.Feedback.ReplyText, "upload was lost") ||
		!strings.Contains(*out.Feedback.ReplyText, "huge.mov") || !strings.Contains(*out.Feedback.ReplyText, "too large") {
		t.Fatalf("feedback: %+v", out.Feedback)
	}
	n := ch.get(*out.NoteID)
	if len(n.Parts) != 3 || n.Parts[1].Kind != "failed_attachment" || n.Parts[1].Failed.Reason != "upload_missing" ||
		n.Parts[2].Failed == nil || n.Parts[2].Failed.Reason != "too_large" || ch.texts(n)[0] != "look at this" {
		t.Fatalf("%+v", n.Parts)
	}
	// An upload can be used by one part only: a second reference to it fails instead of sharing the file.
	ph := ch.photo("once.png")
	first := ch.send("$a", 30*time.Minute, ph)
	second := ch.send("$b", time.Hour, ph)
	if first.Result != "created" || ch.get(*second.NoteID).Parts[0].Kind != "failed_attachment" {
		t.Fatalf("reuse: %+v %+v", first, second)
	}
}

// Bot uploads: same limits as the app, only for linked identities, owned by the identity's user (BOT-6, CORE-A3).
func TestBotUploads(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	id := uuid.NewString()
	first := randomBytes(300_000)
	if res := ch.botUpload(id, "ok.png", first); res.Status != 201 {
		t.Fatalf("upload: %d %s", res.Status, res.Body)
	}
	// Repeating the same upload id is not an error, does not double-count storage, and the first
	// bytes stay: a retry after a truncated attempt can never replace what was already stored.
	if res := ch.botUpload(id, "ok.png", randomBytes(300_000)); res.Status != 201 {
		t.Fatalf("repeat: %d %s", res.Status, res.Body)
	}
	if st := s.storageOf("alice"); st.used != 300_000 {
		t.Fatalf("storage: %+v", st)
	}
	// A stranger's file goes nowhere.
	req, _ := http.NewRequest("PUT", s.bot.URL+"/bot/v1/uploads/"+uuid.NewString(), bytes.NewReader([]byte("x")))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-External-User", "@stranger:example.org")
	req.Header.Set("X-Filename", "x.png")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("unlinked uploader: %d", res.StatusCode)
	}
	ch.attachNote(id)
	if dl := ch.c.do("GET", "/api/v1/attachments/"+id, nil); !bytes.Equal(dl.Body, first) {
		t.Fatal("a repeated upload replaced the stored file")
	}
	// The upload belongs to alice: nobody else can link it into a note.
	bob := s.appUser("bob")
	if r := bob.c.do("POST", "/api/v1/notes", map[string]any{"parts": []map[string]any{{"type": "attachment", "attachment_id": id}}}); r.Status == 201 {
		t.Fatalf("bob attached alice's upload: %d", r.Status)
	}
	// Unauthenticated calls are refused.
	if r := s.botDo("", "PUT", "/bot/v1/uploads/"+uuid.NewString(), nil); r.Status != 401 {
		t.Fatalf("no key: %d", r.Status)
	}
}

// A message delivered twice gets the same answer; even after the delivery record is gone the
// message is not stored twice (BOT-7, BOT-B4).
func TestReplayIsHarmless(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	ev := map[string]any{"event_id": "$once", "kind": "message_created", "sender": ch.ext, "conversation": ch.conv, "message_id": "$m1",
		"timestamp": ch.base.Add(time.Second).UTC().Format(time.RFC3339Nano), "parts": []map[string]any{text("hello")}}
	first := ch.post2(ev)
	again := ch.post2(ev)
	if first.Result != "created" || again.Result != "created" || *first.NoteID != *again.NoteID {
		t.Fatalf("%+v %+v", first, again)
	}
	// The record of the delivery is dropped (retention), and the same message arrives under a new event id.
	if _, err := s.db.Admin.Exec(context.Background(), `delete from ingest_events`); err != nil {
		t.Fatal(err)
	}
	ev["event_id"] = "$again-later"
	late := ch.post2(ev)
	if late.NoteID == nil || *late.NoteID != *first.NoteID || ch.notes() != 1 {
		t.Fatalf("replay after retention: %+v, %d notes", late, ch.notes())
	}
	if ch.s.count(`select count(*) from note_parts`) != 1 {
		t.Fatal("the message was stored twice")
	}
}

// Many messages of one chat arriving at once are still grouped exactly as if they arrived in order
// (GRP-5, BOT-B3, CORE-N19: one lock per user).
func TestConcurrentDeliveryGroupsConsistently(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	parts := make([]map[string]any, 8)
	for i := range parts {
		parts[i] = ch.photo(fmt.Sprintf("p%d.png", i))
	}
	var wg sync.WaitGroup
	for i, p := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, _ := json.Marshal(map[string]any{"event_id": fmt.Sprintf("$c%d", i), "kind": "message_created", "sender": ch.ext, "conversation": ch.conv,
				"message_id": fmt.Sprintf("$c%d", i), "timestamp": ch.base.UTC().Format(time.RFC3339Nano), "parts": []map[string]any{p}})
			req, _ := http.NewRequest("POST", s.bot.URL+"/bot/v1/events", bytes.NewReader(b))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = res.Body.Close()
			}
		}()
	}
	wg.Wait()
	// Whatever the order, no photo may be lost, and none may be stored twice.
	if got := s.count(`select count(*) from note_parts`); got != 8 {
		t.Fatalf("parts: %d, want 8", got)
	}
	if got := s.count(`select count(distinct source_message_id) from note_parts`); got != 8 {
		t.Fatalf("distinct messages: %d, want 8", got)
	}
	// Equal timestamps and one sender: whichever event gets the user lock first creates the note and
	// all the others join it, so there is exactly one note.
	if got := ch.notes(); got != 1 {
		t.Fatalf("notes: %d, want 1", got)
	}
}

// An event id that was already used for someone else's message never returns their outcome (BOT-7, SEC-ISO-1).
func TestEventIdsAreNotSharedBetweenUsers(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	alice, bob := s.chatter("alice", key), s.chatter("bob", key)
	ev := func(ch *chatter) map[string]any {
		return map[string]any{"event_id": "$same", "kind": "message_created", "sender": ch.ext, "conversation": ch.conv, "message_id": "$m",
			"timestamp": ch.base.Add(time.Second).UTC().Format(time.RFC3339Nano), "parts": []map[string]any{text("hi")}}
	}
	if out := alice.post2(ev(alice)); out.Result != "created" {
		t.Fatalf("%+v", out)
	}
	res := s.botDo(key, "POST", "/bot/v1/events", ev(bob))
	if res.Status != 400 || bytes.Contains(res.Body, []byte("note_id")) {
		t.Fatalf("reused id: %d %s", res.Status, res.Body)
	}
	if bob.notes2() != 0 {
		t.Fatal("bob must have no note")
	}
}

func (ch *chatter) notes2() int {
	return ch.s.count(`select count(*) from notes n join users u on u.id = n.user_id where u.username = $1`, ch.ext[1:len(ch.ext)-len(":example.org")])
}

// A timestamp far in the future is clamped to the receive time so it cannot pin a note to the top (CORE-N18).
func TestFutureTimestampsAreClamped(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	s.svc.Ingest.Now = time.Now // the real clock again
	out := ch.send("$future", 24*time.Hour, text("from the future"))
	var created time.Time
	if err := s.db.Admin.QueryRow(context.Background(), `select created_at from notes where id = $1`, *out.NoteID).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created.After(time.Now().Add(time.Minute)) {
		t.Fatalf("created_at %v was not clamped", created)
	}
}

// A bot asks Core how far it has got in a conversation, to resume after downtime (BOT-10).
func TestConversationCursor(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	var out struct {
		LastMessageAt *time.Time `json:"last_message_at"`
	}
	s.botDo(key, "GET", "/bot/v1/conversations/"+url.PathEscape(ch.conv)+"/cursor", nil).JSON(t, &out)
	if out.LastMessageAt != nil {
		t.Fatalf("nothing stored yet: %v", out.LastMessageAt)
	}
	ch.send("$a", time.Second, text("one"))
	at := ch.base.Add(3 * time.Second)
	ch.send("$b", 3*time.Second, text("two"))
	ch.send("$c", 2*time.Second, text("older, arrived late"))
	res := s.botDo(key, "GET", "/bot/v1/conversations/"+url.PathEscape(ch.conv)+"/cursor", nil)
	res.JSON(t, &out)
	if res.Status != 200 || out.LastMessageAt == nil || out.LastMessageAt.Sub(at).Abs() > time.Millisecond {
		t.Fatalf("cursor: %d %s (want %v)", res.Status, res.Body, at)
	}
}
