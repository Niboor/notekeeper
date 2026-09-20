//go:build integration

package server_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A large account is removed in many short transactions, and nothing is left behind (CR-031, AUTH-U9).
func TestLargeAccountDeletionRunsInBatches(t *testing.T) {
	s := newStack(t)
	s.makeUser("root", true)
	adminClient := s.newClient()
	adminClient.login("root")
	victim := s.appUser("victim")
	// More notes than one batch holds, each with several parts, a reminder or a history entry.
	const notes = 450
	for i := 0; i < notes; i++ {
		n := victim.note("", fmt.Sprintf("note %d", i), nil)
		if i%50 == 0 {
			victim.remind(n.ID, time.Now().Add(48*time.Hour), "")
		}
	}
	var id uuid.UUID
	_ = s.db.Admin.QueryRow(t.Context(), `select id from users where username = 'victim'`).Scan(&id)
	if res := adminClient.do("DELETE", "/api/v1/admin/users/"+id.String(), nil); res.Status != 202 {
		t.Fatalf("delete: %d %s", res.Status, res.Body)
	}
	if n, err := s.svc.Accounts.ProcessDeletions(t.Context()); err != nil || n != 1 {
		t.Fatalf("process: %d %v", n, err)
	}
	if left := s.rowsOfUser(id); len(left) != 0 {
		t.Fatalf("data left behind: %v", left)
	}
}

// Numeric parameters that the generated handlers do not check are clamped, and text that PostgreSQL
// cannot store is refused with a 400, never a 500 (CR-038, SEC-API-3).
func TestParametersAreClampedAndBadTextRefused(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	u := s.appUser("alice")
	for _, q := range []string{"limit=-1", "limit=0", "limit=100000"} {
		if res := s.botDo(key, "GET", "/bot/v1/outbox?"+q, nil); res.Status != 200 {
			t.Errorf("claim outbox %s: %d %s", q, res.Status, res.Body)
		}
		if res := u.c.do("GET", "/api/v1/changes?cursor=&"+q, nil); res.Status != 200 {
			t.Errorf("changes %s: %d %s", q, res.Status, res.Body)
		}
	}
	if res := u.c.do("POST", "/api/v1/pages", map[string]any{"name": "bad\x00name"}); res.Status != 400 {
		t.Errorf("page name with NUL: %d %s", res.Status, res.Body)
	}
	if res := u.c.do("POST", "/api/v1/notes", map[string]any{"parts": []map[string]any{{"type": "text", "text": "a\x00b"}}}); res.Status != 400 {
		t.Errorf("note text with NUL: %d %s", res.Status, res.Body)
	}
}

// A client whose place in the change feed is gone, or ahead of the feed, is told to refetch (CR-040).
func TestChangeFeedTellsStaleClientsToResync(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	u.note("", "one", nil)
	var cur struct {
		Cursor string `json:"cursor"`
	}
	u.get("/api/v1/changes", &cur)
	old := cur.Cursor
	u.note("", "two", nil)
	// The retention job ran after a quiet month: nothing is left although changes were made.
	if _, err := s.db.Admin.Exec(t.Context(), `delete from changes`); err != nil {
		t.Fatal(err)
	}
	if res := u.c.do("GET", "/api/v1/changes?cursor="+old, nil); res.Status != 410 {
		t.Fatalf("stale cursor after a full purge: %d %s", res.Status, strings.TrimSpace(string(res.Body)))
	}
}
