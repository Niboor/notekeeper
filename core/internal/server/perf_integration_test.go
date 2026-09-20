//go:build integration && perf

package server_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Performance at the sizing the requirements name (NFR-P1, NFR-P2, NFR-P3): one user with tens of
// thousands of notes, read latency p95 at most 300 ms, and a chat message visible in the app within
// three seconds. It is a separate tier (`make test-perf`) because seeding takes a while; NK_PERF_NOTES
// sets the size (default 20 000; the documented target is 50 000).
func TestPerformanceAtTheDocumentedSize(t *testing.T) {
	total := 20_000
	if v, err := strconv.Atoi(os.Getenv("NK_PERF_NOTES")); err == nil && v > 0 {
		total = v
	}
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("perfuser", key)
	uid := uuid.MustParse(ch.userID())
	ctx := context.Background()

	// A page with three columns of 500 notes, the rest in the Inbox, some in the Trash.
	page := ch.page("Big")
	cats := []string{ch.category(page, "A"), ch.category(page, "B"), ch.category(page, "C")}
	seed := time.Now()
	tx, err := s.db.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `select set_config('app.user_id', $1, true)`, uid.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `set local session_replication_role = replica`); err != nil { // no per-row triggers while seeding; the index is built after
		t.Fatal(err)
	}
	for i, c := range cats {
		if _, err := tx.Exec(ctx, `insert into notes (id, user_id, category_id, position, created_at, received_at)
			select gen_random_uuid(), $1, $2, 'k' || lpad(g::text, 6, '0'), now() - (g || ' seconds')::interval, now() from generate_series(1, 500) g`, uid, c); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := tx.Exec(ctx, `insert into notes (id, user_id, state, deleted_at, created_at, received_at)
		select gen_random_uuid(), $1, case when g % 20 = 0 then 'deleted' else 'active' end, case when g % 20 = 0 then now() else null end, now() - (g || ' seconds')::interval - interval '1 day', now()
		from generate_series(1, $2::int) g`, uid, total-1500); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `insert into note_parts (id, user_id, note_id, ordinal, kind, text, attach_reason, created_at)
		select gen_random_uuid(), user_id, id, 0, 'text',
		  'note ' || substr(md5(id::text), 1, 8) || ' about ' || (array['travel','money','garden','kitchen','health','work','music','books'])[1 + (abs(hashtext(id::text)) % 8)]
		  || case when abs(hashtext(id::text)) % 50 = 0 then ' rarezebra' else '' end, 'first', created_at
		from notes where user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `insert into note_search (note_id, user_id, body, doc)
		select n.id, n.user_id, p.text, to_tsvector('english', nk_unaccent(p.text)) from notes n join note_parts p on p.note_id = n.id and p.user_id = n.user_id where n.user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Admin.Exec(ctx, `analyze notes; analyze note_parts; analyze note_search`); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %d notes in %v", total, time.Since(seed).Round(time.Millisecond))

	p95 := func(name string, n int, fn func()) time.Duration {
		fn() // warm the caches, as a running system is
		d := make([]time.Duration, n)
		for i := range d {
			start := time.Now()
			fn()
			d[i] = time.Since(start)
		}
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		got := d[(n*95)/100]
		t.Logf("%-32s p95 %v (max %v)", name, got.Round(time.Millisecond), d[n-1].Round(time.Millisecond))
		return got
	}
	limit := 300 * time.Millisecond
	check := func(name string, got time.Duration) {
		if got > limit {
			t.Errorf("%s: p95 %v exceeds %v (NFR-P2)", name, got, limit)
		}
	}
	var board boardJSON
	check("board of 1500 notes in 3 columns", p95("board", 40, func() { ch.appUser.get("/api/v1/pages/"+page+"/board?notes_per_category=500", &board) }))
	if len(board.Categories) != 3 {
		t.Fatalf("board: %+v", board.Categories)
	}
	var np notePageFull
	check("category listing", p95("category", 40, func() { ch.appUser.get("/api/v1/categories/"+cats[0]+"/notes?limit=100", &np) }))
	check("inbox first page", p95("inbox", 40, func() { ch.appUser.get("/api/v1/inbox/notes?limit=50", &np) }))
	check("trash first page", p95("trash", 40, func() { ch.appUser.get("/api/v1/trash/notes?limit=50", &np) }))
	var sr searchJSON
	check("search, common word", p95("search common", 40, func() { ch.appUser.get("/api/v1/search?q=travel&limit=25", &sr) }))
	check("search, rare word", p95("search rare", 40, func() { ch.appUser.get("/api/v1/search?q=rarezebra&limit=25", &sr) }))
	check("search, two words", p95("search two", 40, func() { ch.appUser.get("/api/v1/search?q=garden+note&limit=25", &sr) }))
	check("search, part of a word", p95("search substring", 20, func() { ch.appUser.get("/api/v1/search?q=ardenn&limit=25", &sr) }))

	// A chat message reaches the open app quickly even with all that data (NFR-P1).
	stream := openSSE(t, s, ch.c)
	defer stream.close()
	if ev := stream.next(t); ev.Name != "hello" {
		t.Fatalf("first event: %+v", ev)
	}
	lat := make([]time.Duration, 20)
	for i := range lat {
		start := time.Now()
		out := ch.send(fmt.Sprintf("$perf%d", i), time.Duration(i)*time.Hour, text(fmt.Sprintf("latency probe %d", i)))
		for {
			ev := stream.until(t, "change")
			if contains(ev.Data, *out.NoteID) {
				break
			}
		}
		lat[i] = time.Since(start)
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	t.Logf("chat message to app          p95 %v (max %v)", lat[19].Round(time.Millisecond), lat[19].Round(time.Millisecond))
	if lat[18] > 3*time.Second {
		t.Errorf("ingest to visible: p95 %v exceeds 3 s (NFR-P1)", lat[18])
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
