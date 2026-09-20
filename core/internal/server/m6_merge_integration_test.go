//go:build integration

package server_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Merging two notes and splitting a part out of one repair a grouping the app got wrong; the parts keep
// their origin, so the chat still finds them (CORE-N14, GRP-10, WEB-15).
func TestMergeAndSplitNotes(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	work := ch.page("Work")
	col := ch.category(work, "Todo")

	a := ch.send("$a", 0, text("call the dentist"))
	b := ch.send("$b", 5*time.Minute, text("renew the passport"))
	ch.post("/api/v1/notes/"+*a.NoteID+"/move", map[string]any{"category_id": col}, 200, nil)
	ch.remind(*b.NoteID, time.Now().Add(time.Hour), "")
	link := ch.share(*b.NoteID, "1d")

	// Merge b into a.
	var merged noteView
	ch.post("/api/v1/notes/"+*a.NoteID+"/merge", map[string]any{"source_id": *b.NoteID}, 200, &merged)
	if got := ch.texts(merged); len(got) != 2 || got[0] != "call the dentist" || got[1] != "renew the passport" {
		t.Fatalf("merged parts: %v", got)
	}
	if res := ch.c.do("GET", "/api/v1/notes/"+*b.NoteID, nil); res.Status != 404 {
		t.Fatalf("the merged-away note: %d", res.Status)
	}
	if got := ch.reminderOf(*a.NoteID); len(got) != 1 {
		t.Fatalf("the reminder must move with the parts: %+v", got)
	}
	if s.publicDo(link.token(t), "GET", "/api/public/v1/share", nil).Status != 404 {
		t.Fatal("a link to the removed note must stop working")
	}
	if got := ch.search("passport"); len(got.Items) != 1 || got.Items[0].Note.ID != *a.NoteID {
		t.Fatalf("search after merge: %+v", got)
	}
	// The chat still reaches the part it created, now inside the other note.
	if out := ch.edit("$b", 10*time.Minute, "renew the passport by friday"); out.Result != "updated" || *out.NoteID != *a.NoteID {
		t.Fatalf("edit after merge: %+v", out)
	}
	if got := ch.texts(ch.get(*a.NoteID)); got[1] != "renew the passport by friday" {
		t.Fatalf("edit did not reach the merged part: %v", got)
	}

	// Split it out again: a note of its own, right below the first, with the part's own time.
	var res struct {
		Source  noteView `json:"source"`
		Created struct {
			ID         string  `json:"id"`
			CategoryID *string `json:"category_id"`
			Parts      []struct {
				Text *string `json:"text"`
			} `json:"parts"`
		} `json:"created"`
	}
	ch.post("/api/v1/notes/"+*a.NoteID+"/parts/"+merged.Parts[1].ID+"/split", nil, 200, &res)
	if len(res.Source.Parts) != 1 || len(res.Created.Parts) != 1 || *res.Created.Parts[0].Text != "renew the passport by friday" || res.Created.CategoryID == nil || *res.Created.CategoryID != col {
		t.Fatalf("split: %+v", res)
	}
	column := ch.column(work, 0)
	if len(column) != 2 || column[0].ID != *a.NoteID || column[1].ID != res.Created.ID {
		t.Fatalf("the new note must sit right below the original: %+v", column)
	}
	if out := ch.edit("$b", 20*time.Minute, "renew the passport for good"); out.Result != "updated" || *out.NoteID != res.Created.ID {
		t.Fatalf("edit after split: %+v", out)
	}
	// A note keeps at least one part; a note cannot be merged into itself; foreign and trashed notes are refused.
	if r := ch.c.do("POST", "/api/v1/notes/"+*a.NoteID+"/parts/"+res.Source.Parts[0].ID+"/split", nil); r.Status != 400 {
		t.Fatalf("split of the only part: %d", r.Status)
	}
	if r := ch.c.do("POST", "/api/v1/notes/"+*a.NoteID+"/merge", map[string]any{"source_id": *a.NoteID}); r.Status != 400 {
		t.Fatalf("merge into itself: %d", r.Status)
	}
	bob := s.appUser("bob")
	bn := bob.note("", "bob's", nil)
	if r := ch.c.do("POST", "/api/v1/notes/"+*a.NoteID+"/merge", map[string]any{"source_id": bn.ID}); r.Status != 404 {
		t.Fatalf("merging somebody else's note: %d", r.Status)
	}
	if r := bob.c.do("POST", "/api/v1/notes/"+bn.ID+"/merge", map[string]any{"source_id": *a.NoteID}); r.Status != 404 {
		t.Fatalf("merging into somebody else's note: %d", r.Status)
	}
	ch.post("/api/v1/notes/"+res.Created.ID+"/dismiss", nil, 200, nil)
	if r := ch.c.do("POST", "/api/v1/notes/"+*a.NoteID+"/merge", map[string]any{"source_id": res.Created.ID}); r.Status != 409 {
		t.Fatalf("merging a trashed note: %d", r.Status)
	}
	if r := ch.c.do("POST", "/api/v1/notes/"+uuid.NewString()+"/merge", map[string]any{"source_id": *a.NoteID}); r.Status != 404 || strings.Contains(string(r.Body), "alice") {
		t.Fatalf("missing target: %d", r.Status)
	}
}
