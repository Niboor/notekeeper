//go:build integration

package server_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Niboor/notekeeper/core/internal/store"
)

// usage reads what the triggers counted, next to what is really stored.
func (s *stack) usage(user string) (counted, actual struct{ Notes, Text int64 }) {
	s.t.Helper()
	q := `select coalesce(st.note_count, 0), coalesce(st.text_bytes, 0),
	        (select count(*) from notes n where n.user_id = u.id),
	        coalesce((select sum(octet_length(p.text)) from note_parts p where p.user_id = u.id), 0)
	      + coalesce((select sum(octet_length(v.text)) from note_part_versions v where v.user_id = u.id), 0)
	      from users u left join user_storage st on st.user_id = u.id where u.username = $1`
	if err := s.db.Admin.QueryRow(s.t.Context(), q, user).Scan(&counted.Notes, &counted.Text, &actual.Notes, &actual.Text); err != nil {
		s.t.Fatal(err)
	}
	return counted, actual
}

// The counters behind the limits stay exactly equal to what is stored, through every way of writing
// notes: the app, chat, edits, merges, splits and deletions (SEC-API-3, migration 0008).
func TestUsageCountersMatchWhatIsStored(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	check := func(step string) {
		t.Helper()
		if c, a := s.usage("alice"); c != a {
			t.Fatalf("%s: counted %+v, stored %+v", step, c, a)
		}
	}
	n1 := ch.note("", "first note with some text", nil)
	n2 := ch.note("", "second — with ünïcode", nil)
	check("created in the app")
	ch.send("$a", time.Second, text("from chat"))
	ch.edit("$a", 2*time.Second, "from chat, edited")
	check("chat message and edit")
	ch.c.do("PATCH", "/api/v1/notes/"+n1.ID+"/parts/"+n1.Parts[0].ID, map[string]any{"text": "first note, rewritten at length " + strings.Repeat("x", 500)})
	check("edit in the app")
	ch.post("/api/v1/notes/"+n1.ID+"/merge", map[string]any{"source_id": n2.ID}, 200, nil)
	check("merge")
	ch.post("/api/v1/notes/"+n1.ID+"/dismiss", nil, 200, nil)
	ch.del("/api/v1/notes/"+n1.ID, 204)
	check("delete for good")
	ch.post("/api/v1/notes", map[string]any{"parts": []map[string]any{{"type": "text", "text": "a"}, {"type": "text", "text": "b"}}}, 201, nil)
	check("a note with two parts")
}

// One account cannot grow past its note and text limits, in the app or from chat, and can still shrink
// (SEC-API-3, SEC-CNT-6, SR-010).
func TestNoteAndTextLimits(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	s.st.SetLimits(store.Limits{MaxNotes: 3, MaxTextBytes: 400})
	t.Cleanup(func() { s.st.SetLimits(store.Limits{}) })

	a := ch.note("", "one", nil)
	ch.note("", "two", nil)
	ch.note("", "three", nil)
	res := ch.c.do("POST", "/api/v1/notes", map[string]any{"parts": []map[string]any{{"type": "text", "text": "four"}}})
	if res.Status != 413 || res.Code() != "notes_limit" {
		t.Fatalf("the fourth note: %d %s", res.Status, res.Body)
	}
	// From chat the answer is a message the person can read, not an error the bot would retry.
	out := ch.send("$over", time.Second, text("five"))
	if out.Result != "rejected" || out.Code == nil || *out.Code != "storage_full" || out.Feedback.ReplyText == nil || !strings.Contains(*out.Feedback.ReplyText, "full") {
		t.Fatalf("chat message over the limit: %+v", out)
	}
	if cmd := ch.command("remind", "in 2 hours call mum", "", "$c1"); cmd.OK || !strings.Contains(cmd.reply(), "full") {
		t.Fatalf("!remind over the limit: %+v", cmd)
	}
	// Deleting for good makes room.
	ch.post("/api/v1/notes/"+a.ID+"/dismiss", nil, 200, nil)
	ch.del("/api/v1/notes/"+a.ID, 204)
	ch.note("", "four", nil)

	// Text: a note that would take the account past its text budget is refused, and editing downwards works.
	s.st.SetLimits(store.Limits{MaxNotes: 100, MaxTextBytes: 200})
	big := ch.c.do("POST", "/api/v1/notes", map[string]any{"parts": []map[string]any{{"type": "text", "text": strings.Repeat("y", 300)}}})
	if big.Status != 413 || big.Code() != "text_limit" {
		t.Fatalf("a note over the text limit: %d %s", big.Status, big.Body)
	}
	s.st.SetLimits(store.Limits{MaxNotes: 100, MaxTextBytes: 100})
	first := ch.inbox()[0]
	if r := ch.c.do("PATCH", "/api/v1/notes/"+first.ID+"/parts/"+first.Parts[0].ID, map[string]any{"text": "x"}); r.Status != 200 {
		t.Fatalf("shrinking an edit was refused: %d %s", r.Status, r.Body)
	}
}

// The history of one part is capped, oldest first (SR-010).
func TestVersionHistoryIsCapped(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n := u.note("", "history", nil)
	if _, err := s.db.Admin.Exec(t.Context(), fmt.Sprintf(`insert into note_part_versions (id, user_id, part_id, text, origin, edited_at, applied)
		select gen_random_uuid(), p.user_id, p.id, 'v' || g, 'app', now() - (g || ' minutes')::interval, true
		from note_parts p, generate_series(1, 250) g where p.id = '%s'`, n.Parts[0].ID)); err != nil {
		t.Fatal(err)
	}
	var count int
	var hasNewest, hasOldest bool
	if err := s.db.Admin.QueryRow(t.Context(), `select count(*), bool_or(text = 'history'), bool_or(text = 'v250') from note_part_versions where part_id = $1`, n.Parts[0].ID).Scan(&count, &hasNewest, &hasOldest); err != nil {
		t.Fatal(err)
	}
	if count != 200 || !hasNewest || hasOldest {
		t.Fatalf("history holds %d versions (newest kept %v, oldest kept %v): want the newest 200", count, hasNewest, hasOldest)
	}
	if c, a := s.usage("alice"); c != a {
		t.Fatalf("counters after pruning: %+v vs %+v", c, a)
	}
}
