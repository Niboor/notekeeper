//go:build integration

package server_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func readZip(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a valid archive: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[f.Name] = b
	}
	return out
}

// The export holds everything the person has, in open formats, and nothing of anybody else's; the
// share tokens are not in it; asking too often is refused; and every export is audited (AUTH-U5,
// SEC-DATA-8, NFR-S7).
func TestExportHoldsExactlyTheUsersOwnData(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	other := s.appUser("bob")
	other.noteWithFile("bob's private note", "bob-secret.txt", "text/plain", []byte("BOB-FILE-CONTENT"))
	other.page("Bobs page")

	work := u.page("Work")
	cat := u.category(work, "Todo")
	n1 := u.note(cat, "first draft", nil)
	u.c.do("PATCH", "/api/v1/notes/"+n1.ID+"/parts/"+n1.Parts[0].ID, map[string]any{"text": "second draft"})
	file := randomBytes(600_000)
	n2, att := u.noteWithFile("with a file", "ticket photo.png", "image/png", file)
	trashed := u.note("", "in the trash", nil)
	u.post("/api/v1/notes/"+trashed.ID+"/dismiss", nil, 200, nil)
	u.remind(n2.ID, time.Now().Add(time.Hour), "FREQ=DAILY")
	link := u.share(n2.ID, "1d")

	res := u.c.do("GET", "/api/v1/me/export", nil)
	if res.Status != 200 || res.Header.Get("Content-Type") != "application/zip" || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("export: %d %v", res.Status, res.Header)
	}
	files := readZip(t, res.Body)
	var doc struct {
		Format string           `json:"format"`
		Pages  []map[string]any `json:"pages"`
		Cats   []map[string]any `json:"categories"`
		Notes  []struct {
			ID    string `json:"id"`
			State string `json:"state"`
			Parts []struct {
				Text       *string                    `json:"text"`
				Attachment *struct{ ID, File string } `json:"attachment"`
				History    []struct {
					Text, Origin string
				} `json:"history"`
			} `json:"parts"`
		} `json:"notes"`
		Reminders  []map[string]any `json:"reminders"`
		ShareLinks []map[string]any `json:"share_links"`
	}
	if err := json.Unmarshal(files["export.json"], &doc); err != nil {
		t.Fatalf("export.json: %v", err)
	}
	if doc.Format != "notekeeper-export" || len(doc.Pages) != 1 || len(doc.Cats) != 1 || len(doc.Notes) != 3 || len(doc.Reminders) != 1 || len(doc.ShareLinks) != 1 {
		t.Fatalf("contents: %s", files["export.json"])
	}
	states := map[string]int{}
	var sawHistory bool
	for _, n := range doc.Notes {
		states[n.State]++
		for _, p := range n.Parts {
			if p.Text != nil && *p.Text == "second draft" && len(p.History) >= 2 {
				sawHistory = true // the earlier text is kept with the note
			}
			if p.Attachment != nil {
				if !bytes.Equal(files[p.Attachment.File], file) {
					t.Fatalf("the file %s in the archive differs from the upload", p.Attachment.File)
				}
				if !strings.HasPrefix(p.Attachment.File, "attachments/"+att+"-ticket photo.png") {
					t.Fatalf("file name: %s", p.Attachment.File)
				}
			}
		}
	}
	if states["active"] != 2 || states["deleted"] != 1 || !sawHistory {
		t.Fatalf("states %v, history %v", states, sawHistory)
	}
	// Nothing of Bob's, in the text or in the files; and no share token in any form.
	all, words := "", ""
	for name, b := range files {
		all += name + string(b)
		if !strings.HasPrefix(name, "attachments/") { // the attachment above is 600 KB of random bytes, which spell "bob" by chance
			words += name + string(b)
		}
	}
	token := link.token(t)
	for _, foreign := range []string{"bob's private note", "bob-secret", "BOB-FILE-CONTENT", "Bobs page"} {
		if strings.Contains(all, foreign) {
			t.Fatalf("the export holds something of another user: %q", foreign)
		}
	}
	if strings.Contains(words, "bob") {
		t.Fatal("the export holds something of another user: \"bob\"")
	}
	if strings.Contains(all, token) || strings.Contains(string(files["export.json"]), "token") {
		t.Fatal("the export holds a share token")
	}

	// It is audited, and asking again and again is refused.
	if s.count(`select count(*) from audit_log where action = 'user.exported'`) != 1 {
		t.Fatal("the export was not audited")
	}
	var last int
	for range 6 {
		last = u.c.do("GET", "/api/v1/me/export", nil).Status
	}
	if last != 429 {
		t.Fatalf("the limit never applied: %d", last)
	}
	if other.c.do("GET", "/api/v1/me/export", nil).Status != 200 {
		t.Fatal("one user's limit must not affect another's")
	}
	if anon := s.newClient().do("GET", "/api/v1/me/export", nil); anon.Status != 401 {
		t.Fatalf("anonymous: %d", anon.Status)
	}
}
