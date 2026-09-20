//go:build integration

package server_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type searchJSON struct {
	Items []struct {
		Note    noteJSON `json:"note"`
		Snippet string   `json:"snippet"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func (u *appUser) search(q string, extra ...string) searchJSON {
	u.t.Helper()
	path := "/api/v1/search?q=" + url.QueryEscape(q)
	for _, e := range extra {
		path += "&" + e
	}
	var r searchJSON
	u.get(path, &r)
	return r
}

func (r searchJSON) texts() string {
	var out []string
	for _, h := range r.Items {
		out = append(out, h.Note.text())
	}
	return strings.Join(out, " | ")
}

func TestSearchFindsWordsPrefixesAccentsAndStems(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	u.note("", "Concert on Friday, doors at 19:30", nil)
	u.note("", "Grab a café au lait after the run", nil)
	u.note("", "Running shoes: size 43", nil)
	u.note("", "Unrelated shopping list", nil)

	for q, want := range map[string]string{
		"concert":     "Concert on Friday, doors at 19:30",
		"conc fri":    "Concert on Friday, doors at 19:30", // prefixes of several words
		"CAFE":        "Grab a café au lait after the run", // case and accents
		"café":        "Grab a café au lait after the run",
		"shoe":        "Running shoes: size 43", // prefix
		"shoes":       "Running shoes: size 43", // plural
		"friday 1930": "",                       // all words must match
	} {
		if got := u.search(q).texts(); got != want {
			t.Errorf("search %q = %q, want %q", q, got, want)
		}
	}
	// English stemming: "running" also finds "run".
	got := u.search("running").texts()
	if !strings.Contains(got, "run") || !strings.Contains(got, "Running shoes") {
		t.Errorf("stemming: %q", got)
	}
	// Matches are marked with private-use characters, never with markup.
	snip := u.search("concert").Items[0].Snippet
	if !strings.Contains(snip, "Concert") || strings.ContainsAny(snip, "<>") {
		t.Errorf("snippet %q", snip)
	}
	// Nothing matches an empty or symbol-only query, and that is not an error.
	for _, q := range []string{"   ", "!!!", "&&&|||"} {
		if r := u.search(q); len(r.Items) != 0 {
			t.Errorf("query %q returned %d hits", q, len(r.Items))
		}
	}
}

func TestSearchIsUpdatedWithEveryChange(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n := u.note("", "buy milk", nil)
	if got := u.search("milk").texts(); got != "buy milk" {
		t.Fatalf("new note: %q", got)
	}
	u.c.do("PATCH", "/api/v1/notes/"+n.ID+"/parts/"+n.Parts[0].ID, map[string]any{"text": "buy oat drink"})
	if len(u.search("milk").Items) != 0 || u.search("oat").texts() != "buy oat drink" {
		t.Fatal("an edit must be searchable at once and the old text must not match")
	}
	u.post("/api/v1/notes/"+n.ID+"/parts", map[string]any{"type": "text", "text": "and bananas"}, 201, nil)
	if len(u.search("bananas").Items) != 1 {
		t.Fatal("an added part is searchable")
	}
	// Attachment file names are searched too.
	id := uuid.NewString()
	u.upload(id, "boarding-pass.pdf", "application/pdf", []byte("%PDF-1.4 fake"))
	m := u.attachNote(id)
	if got := u.search("boarding"); len(got.Items) != 1 || got.Items[0].Note.ID != m.ID {
		t.Fatalf("attachment name: %+v", got)
	}
	if got := u.search("pass", "has_attachment=true"); len(got.Items) != 1 {
		t.Fatalf("has_attachment filter: %+v", got)
	}
	if got := u.search("oat", "has_attachment=true"); len(got.Items) != 0 {
		t.Fatal("has_attachment=true must exclude notes without attachments")
	}
	// Dismissed notes leave the default results and appear in the Trash scope (CORE-N13).
	u.post("/api/v1/notes/"+n.ID+"/dismiss", nil, 200, nil)
	if len(u.search("oat").Items) != 0 {
		t.Fatal("a dismissed note is still in the default results")
	}
	if len(u.search("oat", "scope=trash").Items) != 1 || len(u.search("oat", "scope=all").Items) != 1 {
		t.Fatal("scope=trash and scope=all must find a dismissed note")
	}
	u.post("/api/v1/notes/"+n.ID+"/restore", nil, 200, nil)
	if len(u.search("oat").Items) != 1 {
		t.Fatal("a restored note is searchable again")
	}
	// Permanent deletion removes the index entry.
	u.post("/api/v1/notes/"+n.ID+"/dismiss", nil, 200, nil)
	u.c.do("DELETE", "/api/v1/notes/"+n.ID, nil)
	if len(u.search("oat", "scope=all").Items) != 0 || s.count(`select count(*) from note_search where body like '%oat%'`) != 0 {
		t.Fatal("the search row survived permanent deletion (SEC-DATA-5)")
	}
}

func TestSearchFiltersAndPaging(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	work, home := u.page("Work"), u.page("Home")
	a, b := u.category(work, "A"), u.category(home, "B")
	for i := range 5 {
		u.note(a, fmt.Sprintf("report %d", i), nil)
	}
	u.note(b, "report at home", nil)
	if got := len(u.search("report", "category_id="+a).Items); got != 5 {
		t.Fatalf("category filter: %d", got)
	}
	if got := len(u.search("report", "page_id="+home).Items); got != 1 {
		t.Fatalf("page filter: %d", got)
	}
	// Results carry where each note lives.
	if hit := u.search("home", "page_id="+home).Items[0]; hit.Note.PreviousLocation == nil || hit.Note.PreviousLocation.PageName != "Home" {
		t.Fatalf("location: %+v", hit.Note.PreviousLocation)
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		extra := []string{"limit=4"}
		if cursor != "" {
			extra = append(extra, "cursor="+cursor)
		}
		r := u.search("report", extra...)
		for _, h := range r.Items {
			if seen[h.Note.ID] {
				t.Fatalf("note %s on two pages", h.Note.ID)
			}
			seen[h.Note.ID] = true
		}
		if r.NextCursor == nil {
			break
		}
		cursor = *r.NextCursor
		if pages > 5 {
			t.Fatal("paging does not end")
		}
	}
	if len(seen) != 6 {
		t.Fatalf("paged through %d results, want 6", len(seen))
	}
	if res := u.c.do("GET", "/api/v1/search?q=report&cursor=garbage", nil); res.Status != 400 {
		t.Fatalf("bad cursor: %d", res.Status)
	}
	if res := u.c.do("GET", "/api/v1/search?q=report&scope=everything", nil); res.Status != 400 {
		t.Fatalf("bad scope: %d", res.Status)
	}
}

func TestSearchFallsBackToPartOfAWord(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	u.note("", "Extraordinary concert tickets", nil)
	// "ncert" is not a prefix of any word, so the word search finds nothing and the substring fallback does.
	if got := u.search("ncert").texts(); got != "Extraordinary concert tickets" {
		t.Fatalf("fallback: %q", got)
	}
	// Wildcard characters in the query are data, not patterns.
	if got := u.search("%%%").texts(); got != "" {
		t.Fatalf("wildcards: %q", got)
	}
}

// Nothing typed into the search box can break the query or reach another user's notes (SEC-API-1, SEC-ISO-2).
func TestSearchIsInjectionSafeAndIsolated(t *testing.T) {
	s := newStack(t)
	alice, bob := s.appUser("alice"), s.appUser("bob")
	alice.note("", "alice secret plan", nil)
	bob.note("", "bob secret plan", nil)
	if got := alice.search("secret").texts(); got != "alice secret plan" {
		t.Fatalf("alice sees %q", got)
	}
	if got := bob.search("secret").texts(); got != "bob secret plan" {
		t.Fatalf("bob sees %q", got)
	}
	for _, q := range []string{
		`'; drop table notes; --`, `a' | b:*`, `secret:* & !nothing`, `(secret)`, `\`, `"quoted phrase"`, `secret <-> plan`,
		strings.Repeat("a", 5000), strings.Repeat("word ", 500), string(rune(0)), "🙂🙂🙂",
	} {
		res := alice.c.do("GET", "/api/v1/search?q="+url.QueryEscape(q), nil)
		if res.Status == 500 || (res.Status != 200 && res.Status != 400) {
			t.Errorf("query %q: %d %s", q, res.Status, res.Body)
		}
	}
	if s.count(`select count(*) from notes`) != 2 {
		t.Fatal("a notes table was harmed")
	}
}
