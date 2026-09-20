//go:build integration

package server_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type noteJSON struct {
	ID         string  `json:"id"`
	CategoryID *string `json:"category_id"`
	State      string  `json:"state"`
	Version    int     `json:"version"`
	Parts      []struct {
		ID           string  `json:"id"`
		Kind         string  `json:"kind"`
		Text         *string `json:"text"`
		AttachReason string  `json:"attach_reason"`
	} `json:"parts"`
	PreviousLocation *struct {
		CategoryName string `json:"category_name"`
		PageName     string `json:"page_name"`
	} `json:"previous_location"`
}

type boardJSON struct {
	Page struct {
		ID string `json:"id"`
	} `json:"page"`
	InboxTotal int `json:"inbox_total"`
	Categories []struct {
		Category struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"category"`
		Notes []noteJSON `json:"notes"`
		Total int        `json:"total"`
	} `json:"categories"`
}

func (n noteJSON) text() string {
	if len(n.Parts) == 0 || n.Parts[0].Text == nil {
		return ""
	}
	return *n.Parts[0].Text
}

func texts(ns []noteJSON) string {
	var out []string
	for _, n := range ns {
		out = append(out, n.text())
	}
	return strings.Join(out, ",")
}

// appUser is a signed-in user with helpers for the organising API.
type appUser struct {
	t *testing.T
	c *client
	s *stack
}

func (s *stack) appUser(name string) *appUser {
	s.makeUser(name, false)
	c := s.newClient()
	c.login(name)
	return &appUser{t: s.t, c: c, s: s}
}

func (u *appUser) post(path string, body any, want int, into any) {
	u.t.Helper()
	res := u.c.do("POST", path, body)
	if res.Status != want {
		u.t.Fatalf("POST %s: %d %s (want %d)", path, res.Status, res.Body, want)
	}
	if into != nil {
		res.JSON(u.t, into)
	}
}

func (u *appUser) get(path string, into any) {
	u.t.Helper()
	res := u.c.do("GET", path, nil)
	if res.Status != 200 {
		u.t.Fatalf("GET %s: %d %s", path, res.Status, res.Body)
	}
	res.JSON(u.t, into)
}

func (u *appUser) page(name string) string {
	var p struct {
		ID string `json:"id"`
	}
	u.post("/api/v1/pages", map[string]any{"name": name}, 201, &p)
	return p.ID
}

func (u *appUser) category(page, name string) string {
	var c struct {
		ID string `json:"id"`
	}
	u.post("/api/v1/categories", map[string]any{"page_id": page, "name": name}, 201, &c)
	return c.ID
}

// note creates a text note; category "" means the Inbox.
func (u *appUser) note(category, text string, extra map[string]any) noteJSON {
	body := map[string]any{"parts": []map[string]any{{"type": "text", "text": text}}}
	if category != "" {
		body["category_id"] = category
	}
	for k, v := range extra {
		body[k] = v
	}
	var n noteJSON
	u.post("/api/v1/notes", body, 201, &n)
	return n
}

func (u *appUser) board(page string) boardJSON {
	var b boardJSON
	u.get("/api/v1/pages/"+page+"/board", &b)
	return b
}

func (u *appUser) column(page string, index int) []noteJSON {
	return u.board(page).Categories[index].Notes
}

func (u *appUser) inbox() []noteJSON {
	var p notePageFull
	u.get("/api/v1/inbox/notes", &p)
	return p.Items
}

type notePageFull struct {
	Items      []noteJSON `json:"items"`
	NextCursor *string    `json:"next_cursor"`
	Total      *int       `json:"total"`
}

// Pages and categories: create, rename, reorder, archive, move between pages (CORE-P1, CORE-P2, CORE-P3, CORE-S5).
func TestPagesAndCategories(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")

	work, chores, travel := u.page("Work"), u.page("Chores"), u.page("Travel")
	var pages struct{ Items []struct{ ID, Name string } }
	u.get("/api/v1/pages", &pages)
	if len(pages.Items) != 3 || pages.Items[0].Name != "Work" || pages.Items[2].Name != "Travel" {
		t.Fatalf("new pages go to the end: %+v", pages)
	}

	// Reorder: move Travel to the front, rename Chores, archive Work.
	if res := u.c.do("PATCH", "/api/v1/pages/"+travel, map[string]any{"before_id": work}); res.Status != 200 {
		t.Fatalf("reorder: %d %s", res.Status, res.Body)
	}
	if res := u.c.do("PATCH", "/api/v1/pages/"+chores, map[string]any{"name": "  Home  "}); res.Status != 200 {
		t.Fatal(res.Status, string(res.Body))
	}
	u.c.do("PATCH", "/api/v1/pages/"+work, map[string]any{"archived": true})
	u.get("/api/v1/pages", &pages)
	names := []string{}
	for _, p := range pages.Items {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "Travel,Work,Home" {
		t.Fatalf("page order/names: %v", names)
	}
	if res := u.c.do("PATCH", "/api/v1/pages/"+work, map[string]any{"name": "   "}); res.Status != 400 {
		t.Fatalf("blank name: %d", res.Status)
	}

	// A client-generated id makes creation idempotent; the same id with another name conflicts.
	id := uuid.NewString()
	u.post("/api/v1/pages", map[string]any{"id": id, "name": "Once"}, 201, nil)
	u.post("/api/v1/pages", map[string]any{"id": id, "name": "Once"}, 201, nil)
	u.post("/api/v1/pages", map[string]any{"id": id, "name": "Different"}, 409, nil)
	u.get("/api/v1/pages", &pages)
	if len(pages.Items) != 4 {
		t.Fatalf("idempotent create made %d pages", len(pages.Items))
	}

	// Categories keep their order, can be reordered, and move to another page.
	a, b, c := u.category(work, "Todo"), u.category(work, "Doing"), u.category(work, "Done")
	u.c.do("PATCH", "/api/v1/categories/"+c, map[string]any{"before_id": a})
	board := u.board(work)
	var order []string
	for _, cat := range board.Categories {
		order = append(order, cat.Category.Name)
	}
	if strings.Join(order, ",") != "Done,Todo,Doing" {
		t.Fatalf("category order: %v", order)
	}
	if res := u.c.do("PATCH", "/api/v1/categories/"+b, map[string]any{"page_id": travel}); res.Status != 200 {
		t.Fatalf("move category: %d %s", res.Status, res.Body)
	}
	if got := len(u.board(travel).Categories); got != 1 {
		t.Fatalf("travel categories: %d", got)
	}
	if got := len(u.board(work).Categories); got != 2 {
		t.Fatalf("work categories: %d", got)
	}
}

// Notes go where they are put and stay there: creation into columns, dragging between and within
// them, back to the Inbox (CORE-N3, CORE-N4, WEB-9).
func TestNotesInColumnsAndOrder(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	page := u.page("Work")
	todo, done := u.category(page, "Todo"), u.category(page, "Done")

	// New notes go to the top of a column; the Inbox is ordered by time.
	n1 := u.note(todo, "one", nil)
	n2 := u.note(todo, "two", nil)
	n3 := u.note(todo, "three", nil)
	if got := texts(u.column(page, 0)); got != "three,two,one" {
		t.Fatalf("top-of-column insertion: %s", got)
	}
	// A note created between two neighbours.
	u.note(todo, "between", map[string]any{"after_id": n3.ID, "before_id": n2.ID})
	if got := texts(u.column(page, 0)); got != "three,between,two,one" {
		t.Fatalf("between insertion: %s", got)
	}
	inboxNote := u.note("", "inbox one", nil)
	if inboxNote.CategoryID != nil || len(u.inbox()) != 1 {
		t.Fatalf("inbox note: %+v", inboxNote)
	}

	// Drag from the Inbox into the middle of a column, then reorder inside it, then across columns.
	var moved noteJSON
	u.post("/api/v1/notes/"+inboxNote.ID+"/move", map[string]any{"category_id": todo, "after_id": n2.ID, "before_id": n1.ID}, 200, &moved)
	if got := texts(u.column(page, 0)); got != "three,between,two,inbox one,one" {
		t.Fatalf("move into the middle: %s", got)
	}
	if len(u.inbox()) != 0 {
		t.Fatal("moved note still in the Inbox")
	}
	u.post("/api/v1/notes/"+n1.ID+"/move", map[string]any{"category_id": todo, "before_id": n3.ID}, 200, nil) // to the very top
	if got := texts(u.column(page, 0)); got != "one,three,between,two,inbox one" {
		t.Fatalf("reorder to top: %s", got)
	}
	u.post("/api/v1/notes/"+n2.ID+"/move", map[string]any{"category_id": done}, 200, nil) // no hints: top of the other column
	u.post("/api/v1/notes/"+n3.ID+"/move", map[string]any{"category_id": done, "after_id": n2.ID}, 200, nil)
	if got := texts(u.column(page, 1)); got != "two,three" {
		t.Fatalf("across columns: %s", got)
	}
	// Back to the Inbox: no position, ordered by creation time again.
	u.post("/api/v1/notes/"+n2.ID+"/move", map[string]any{"category_id": nil}, 200, nil)
	if in := u.inbox(); len(in) != 1 || in[0].text() != "two" {
		t.Fatalf("back to the inbox: %s", texts(in))
	}

	// The board reports counts, and the category listing pages through in manual order.
	board := u.board(page)
	if board.Categories[0].Total != 3 || board.Categories[1].Total != 1 || board.InboxTotal != 1 {
		t.Fatalf("counts: %+v", board)
	}
	var first notePageFull
	u.get("/api/v1/categories/"+todo+"/notes?limit=2", &first)
	var second notePageFull
	u.get("/api/v1/categories/"+todo+"/notes?limit=2&cursor="+*first.NextCursor, &second)
	if texts(first.Items) != "one,between" || texts(second.Items) != "inbox one" || second.NextCursor != nil {
		t.Fatalf("paging: %s | %s", texts(first.Items), texts(second.Items))
	}
}

// Deleting a category or page never destroys notes (CORE-P4, CORE-N7).
func TestDeletingACategoryOrPageNeverLosesNotes(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	page := u.page("Work")
	cat, other := u.category(page, "Todo"), u.category(page, "Other")
	u.note(cat, "a", nil)
	u.note(cat, "b", nil)
	dismissed := u.note(cat, "c", nil)
	u.post("/api/v1/notes/"+dismissed.ID+"/dismiss", nil, 200, nil)
	u.note(other, "d", nil)

	var start struct{ Cursor string }
	u.get("/api/v1/changes", &start)

	var del struct {
		MovedNotes int `json:"moved_notes"`
	}
	res := u.c.do("DELETE", "/api/v1/categories/"+cat, nil)
	res.JSON(t, &del)
	if res.Status != 200 || del.MovedNotes != 2 {
		t.Fatalf("delete category: %d %s", res.Status, res.Body)
	}
	if got := texts(u.inbox()); !strings.Contains(got, "a") || !strings.Contains(got, "b") || len(u.inbox()) != 2 {
		t.Fatalf("inbox after category delete: %s", got)
	}
	// Every client learns of the moved notes through the change feed (CORE-S3, CORE-P4).
	var feed struct {
		Items []struct {
			EntityType string `json:"entity_type"`
			Op         string `json:"op"`
		} `json:"items"`
	}
	u.get("/api/v1/changes?cursor="+start.Cursor, &feed)
	notesChanged, catDeleted := 0, false
	for _, it := range feed.Items {
		if it.EntityType == "note" && it.Op == "upsert" {
			notesChanged++
		}
		if it.EntityType == "category" && it.Op == "delete" {
			catDeleted = true
		}
	}
	if notesChanged != 3 || !catDeleted { // two active and one dismissed note were freed
		t.Fatalf("change feed after deleting a category: %+v", feed)
	}
	// A dismissed note whose category is gone is restored into the Inbox (CORE-N7).
	var restored noteJSON
	u.post("/api/v1/notes/"+dismissed.ID+"/restore", nil, 200, &restored)
	if restored.CategoryID != nil || restored.State != "active" {
		t.Fatalf("restore into a deleted category: %+v", restored)
	}

	// Deleting a page returns the notes of all its categories.
	if res := u.c.do("DELETE", "/api/v1/pages/"+page, nil); res.Status != 200 {
		t.Fatalf("delete page: %d", res.Status)
	}
	if len(u.inbox()) != 4 {
		t.Fatalf("inbox after page delete: %s", texts(u.inbox()))
	}
}

// Dismissing, undoing (from any device, since undo is a server-side restore), the Trash view and
// permanent deletion (CORE-N6, CORE-N7, CORE-N8, CORE-N9, CORE-N10).
func TestDismissRestoreAndTrash(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	page := u.page("Work")
	cat := u.category(page, "Todo")
	a, b := u.note(cat, "a", nil), u.note(cat, "b", nil)

	var d noteJSON
	u.post("/api/v1/notes/"+b.ID+"/dismiss", nil, 200, &d)
	if d.State != "deleted" {
		t.Fatalf("dismiss: %+v", d)
	}
	if got := texts(u.column(page, 0)); got != "a" {
		t.Fatalf("dismissed note still on the board: %s", got)
	}
	// Dismissing twice, and moving a dismissed note, are conflicts, not silent successes.
	u.post("/api/v1/notes/"+b.ID+"/dismiss", nil, 409, nil)
	u.post("/api/v1/notes/"+b.ID+"/move", map[string]any{"category_id": nil}, 409, nil)

	// The Trash shows where it came from (CORE-N9).
	var trash notePageFull
	u.get("/api/v1/trash/notes", &trash)
	if len(trash.Items) != 1 || trash.Items[0].PreviousLocation == nil || trash.Items[0].PreviousLocation.CategoryName != "Todo" || trash.Items[0].PreviousLocation.PageName != "Work" {
		t.Fatalf("trash: %+v", trash)
	}
	// Only notes in the Trash can be deleted permanently (CORE-N10).
	if res := u.c.do("DELETE", "/api/v1/notes/"+a.ID, nil); res.Status != 409 {
		t.Fatalf("permanent delete of an active note: %d", res.Status)
	}
	// Restore puts it back where it was, with its position (CORE-N7).
	u.post("/api/v1/notes/"+b.ID+"/restore", nil, 200, nil)
	if got := texts(u.column(page, 0)); got != "b,a" {
		t.Fatalf("restore: %s", got)
	}
	u.post("/api/v1/notes/"+b.ID+"/restore", nil, 409, nil)

	u.post("/api/v1/notes/"+a.ID+"/dismiss", nil, 200, nil)
	if res := u.c.do("DELETE", "/api/v1/notes/"+a.ID, nil); res.Status != 204 {
		t.Fatalf("permanent delete: %d %s", res.Status, res.Body)
	}
	if res := u.c.do("GET", "/api/v1/notes/"+a.ID, nil); res.Status != 404 {
		t.Fatalf("permanently deleted note still readable: %d", res.Status)
	}
	var parts int
	_ = s.db.Admin.QueryRow(t.Context(), `select count(*) from note_parts where note_id = $1`, a.ID).Scan(&parts)
	if parts != 0 {
		t.Fatal("parts survived the permanent delete")
	}
}

// Latest edit wins and nothing is lost (CORE-S4, EDT-3, EDT-5, CORE-N17).
func TestEditingKeepsHistoryAndFlagsStaleEdits(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n := u.note("", "milk", nil)
	part := n.Parts[0].ID

	var res struct {
		Note  noteJSON `json:"note"`
		Stale bool     `json:"stale"`
	}
	u.c.do("PATCH", "/api/v1/notes/"+n.ID+"/parts/"+part, map[string]any{"text": "milk, eggs", "base_version": n.Version}).JSON(t, &res)
	if res.Stale || res.Note.text() != "milk, eggs" || res.Note.Version != n.Version+1 {
		t.Fatalf("edit: %+v", res)
	}
	// An edit based on an old version is applied (latest wins) but flagged (CORE-S4, EDT-5).
	u.c.do("PATCH", "/api/v1/notes/"+n.ID+"/parts/"+part, map[string]any{"text": "milk, eggs, bread", "base_version": n.Version}).JSON(t, &res)
	if !res.Stale || res.Note.text() != "milk, eggs, bread" {
		t.Fatalf("stale edit: %+v", res)
	}
	// Rapid edits of one part in one session are one history entry (CORE-N17): the creation and the coalesced edits.
	var h struct {
		Parts []struct {
			Versions []struct {
				Text, Origin string
				Applied      bool
			} `json:"versions"`
		} `json:"parts"`
	}
	u.get("/api/v1/notes/"+n.ID+"/history", &h)
	if len(h.Parts) != 1 || len(h.Parts[0].Versions) != 2 || h.Parts[0].Versions[0].Text != "milk, eggs, bread" || h.Parts[0].Versions[1].Text != "milk" {
		t.Fatalf("history: %+v", h)
	}
	// After the coalescing window a new edit is a new entry: nothing is lost.
	if _, err := s.db.Admin.Exec(t.Context(), `update note_part_versions set recorded_at = recorded_at - interval '5 minutes' where origin = 'app'`); err != nil {
		t.Fatal(err)
	}
	u.c.do("PATCH", "/api/v1/notes/"+n.ID+"/parts/"+part, map[string]any{"text": "milk, eggs, bread, jam"})
	u.get("/api/v1/notes/"+n.ID+"/history", &h)
	if len(h.Parts[0].Versions) != 3 {
		t.Fatalf("history after the window: %+v", h)
	}
	// Editing to the same text changes nothing: no new version of the note, no history entry.
	var same struct {
		Note  noteJSON `json:"note"`
		Stale bool     `json:"stale"`
	}
	u.c.do("PATCH", "/api/v1/notes/"+n.ID+"/parts/"+part, map[string]any{"text": "milk, eggs, bread, jam", "base_version": res.Note.Version + 5}).JSON(t, &same)
	var latest noteJSON
	u.get("/api/v1/notes/"+n.ID, &latest)
	if latest.Version != same.Note.Version || same.Note.Version != latest.Version {
		t.Fatalf("a no-op edit changed the version: %d", latest.Version)
	}
	versionsBefore := len(h.Parts[0].Versions)
	u.get("/api/v1/notes/"+n.ID+"/history", &h)
	if len(h.Parts[0].Versions) != versionsBefore {
		t.Fatalf("a no-op edit added a history entry: %d -> %d", versionsBefore, len(h.Parts[0].Versions))
	}
	// Bad input.
	if r := u.c.do("PATCH", "/api/v1/notes/"+n.ID+"/parts/"+part, map[string]any{"text": ""}); r.Status != 400 {
		t.Fatalf("empty text: %d", r.Status)
	}
	if r := u.c.do("PATCH", "/api/v1/notes/"+n.ID+"/parts/"+uuid.NewString(), map[string]any{"text": "x"}); r.Status != 404 {
		t.Fatalf("unknown part: %d", r.Status)
	}
}

func TestPartsCanBeAddedAndRemoved(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n := u.note("", "first", nil)
	var withTwo noteJSON
	u.post("/api/v1/notes/"+n.ID+"/parts", map[string]any{"type": "text", "text": "second"}, 201, &withTwo)
	if len(withTwo.Parts) != 2 || withTwo.Parts[1].AttachReason != "app" {
		t.Fatalf("add part: %+v", withTwo)
	}
	var one noteJSON
	if res := u.c.do("DELETE", "/api/v1/notes/"+n.ID+"/parts/"+withTwo.Parts[0].ID, nil); res.Status != 200 {
		t.Fatal(res.Status, string(res.Body))
	} else {
		res.JSON(t, &one)
	}
	if len(one.Parts) != 1 || one.text() != "second" {
		t.Fatalf("after removal: %+v", one)
	}
	// A note always keeps at least one part (CORE-N1).
	if res := u.c.do("DELETE", "/api/v1/notes/"+n.ID+"/parts/"+one.Parts[0].ID, nil); res.Status != 409 {
		t.Fatalf("removing the last part: %d", res.Status)
	}
}

// Retried creations are safe (CORE-S5) and input is bounded (SEC-API-3).
func TestNoteCreationIsIdempotentAndValidated(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	id := uuid.NewString()
	body := map[string]any{"id": id, "parts": []map[string]any{{"type": "text", "text": "hello"}}}
	var a, b noteJSON
	u.post("/api/v1/notes", body, 201, &a)
	u.post("/api/v1/notes", body, 201, &b)
	if a.ID != b.ID || len(u.inbox()) != 1 {
		t.Fatalf("repeated creation made a second note: %s %s", a.ID, b.ID)
	}
	u.post("/api/v1/notes", map[string]any{"id": id, "parts": []map[string]any{{"type": "text", "text": "different"}}}, 409, nil)
	for name, parts := range map[string][]map[string]any{
		"no parts":   {},
		"empty text": {{"type": "text", "text": "  "}},
		"both":       {{"type": "text", "text": "x", "attachment_id": uuid.NewString()}},
		"too long":   {{"type": "text", "text": strings.Repeat("x", 100_001)}},
		"nul":        {{"type": "text", "text": "a\x00b"}},
	} {
		if res := u.c.do("POST", "/api/v1/notes", map[string]any{"parts": parts}); res.Status != 400 {
			t.Errorf("%s: got %d", name, res.Status)
		}
	}
	// Client-chosen ids are unique per user: another user creating a note under the same id gets
	// their own note, exactly as for an unused id, so the id reveals nothing (SEC-ISO-3).
	bob := s.appUser("bob")
	bob.post("/api/v1/notes", body, 201, nil)
	if len(bob.inbox()) != 1 || len(u.inbox()) != 1 {
		t.Fatal("the same id under two users must produce two independent notes")
	}
	if got := u.inbox()[0].text(); got != "hello" {
		t.Fatalf("alice's note changed: %q", got)
	}
}

// Two devices reorder different notes at the same time: nothing conflicts and nothing is lost (CORE-S4, SEC-API-7).
func TestConcurrentMovesNeverConflict(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	page := u.page("Work")
	a, b := u.category(page, "A"), u.category(page, "B")
	var ids []string
	for i := range 20 {
		ids = append(ids, u.note(a, fmt.Sprintf("n%02d", i), nil).ID)
	}
	var wg sync.WaitGroup
	errs := make(chan string, len(ids))
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := map[string]any{"category_id": b}
			if i%2 == 0 {
				body["category_id"] = a // reorder inside the same column
			}
			if res := u.c.do("POST", "/api/v1/notes/"+id+"/move", body); res.Status != 200 {
				errs <- fmt.Sprintf("%s: %d %s", id, res.Status, res.Body)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	board := u.board(page)
	if board.Categories[0].Total+board.Categories[1].Total != 20 {
		t.Fatalf("notes lost or duplicated: %d + %d", board.Categories[0].Total, board.Categories[1].Total)
	}
	seen := map[string]bool{}
	for _, c := range board.Categories {
		for _, n := range c.Notes {
			if seen[n.ID] {
				t.Fatalf("note %s appears twice", n.ID)
			}
			seen[n.ID] = true
		}
	}
}

// Other users' objects look like missing ones, in every operation that names an id (SEC-ISO-2, SEC-ISO-3, SEC-ISO-4).
func TestForeignObjectsAreInvisible(t *testing.T) {
	s := newStack(t)
	alice, bob := s.appUser("alice"), s.appUser("bob")
	page := alice.page("Private")
	cat := alice.category(page, "Secret")
	note := alice.note(cat, "alice only", nil)
	bobPage := bob.page("Mine")
	bobCat := bob.category(bobPage, "Mine")
	bobNote := bob.note(bobCat, "bob's", nil)

	deny := func(name, method, path string, body any) {
		t.Helper()
		res := bob.c.do(method, path, body)
		missing := bob.c.do(method, strings.ReplaceAll(path, "PLACEHOLDER", uuid.NewString()), body)
		_ = missing
		if res.Status != 404 || res.Code() != "not_found" {
			t.Errorf("%s: %d %s (want 404 not_found)", name, res.Status, res.Body)
		}
	}
	deny("read note", "GET", "/api/v1/notes/"+note.ID, nil)
	deny("move note", "POST", "/api/v1/notes/"+note.ID+"/move", map[string]any{"category_id": nil})
	deny("dismiss note", "POST", "/api/v1/notes/"+note.ID+"/dismiss", nil)
	deny("restore note", "POST", "/api/v1/notes/"+note.ID+"/restore", nil)
	deny("delete note", "DELETE", "/api/v1/notes/"+note.ID, nil)
	deny("edit part", "PATCH", "/api/v1/notes/"+note.ID+"/parts/"+note.Parts[0].ID, map[string]any{"text": "pwned"})
	deny("add part", "POST", "/api/v1/notes/"+note.ID+"/parts", map[string]any{"type": "text", "text": "x"})
	deny("history", "GET", "/api/v1/notes/"+note.ID+"/history", nil)
	deny("board", "GET", "/api/v1/pages/"+page+"/board", nil)
	deny("rename page", "PATCH", "/api/v1/pages/"+page, map[string]any{"name": "x"})
	deny("delete page", "DELETE", "/api/v1/pages/"+page, nil)
	deny("category notes", "GET", "/api/v1/categories/"+cat+"/notes", nil)
	deny("rename category", "PATCH", "/api/v1/categories/"+cat, map[string]any{"name": "x"})
	deny("delete category", "DELETE", "/api/v1/categories/"+cat, nil)
	// Referencing another user's objects in one's own requests is refused the same way (SEC-ISO-4).
	deny("create category on a foreign page", "POST", "/api/v1/categories", map[string]any{"page_id": page, "name": "x"})
	deny("create note in a foreign category", "POST", "/api/v1/notes", map[string]any{"category_id": cat, "parts": []map[string]any{{"type": "text", "text": "x"}}})
	deny("move own note to a foreign category", "POST", "/api/v1/notes/"+bobNote.ID+"/move", map[string]any{"category_id": cat})
	deny("move own note next to a foreign note", "POST", "/api/v1/notes/"+bobNote.ID+"/move", map[string]any{"category_id": bobCat, "after_id": note.ID})
	deny("move own category to a foreign page", "PATCH", "/api/v1/categories/"+bobCat, map[string]any{"page_id": page})
	deny("reorder next to a foreign page", "PATCH", "/api/v1/pages/"+bobPage, map[string]any{"after_id": page})

	// Nothing of alice's changed.
	if got := texts(alice.column(page, 0)); got != "alice only" {
		t.Fatalf("alice's column after bob's attempts: %s", got)
	}
	// And alice can still use everything.
	if res := alice.c.do("GET", "/api/v1/notes/"+note.ID, nil); res.Status != 200 {
		t.Fatal(res.Status)
	}
	_ = time.Now
}
