//go:build integration

package server_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/store"
)

type shareLinkJSON struct {
	ID             string     `json:"id"`
	NoteID         string     `json:"note_id"`
	Excerpt        string     `json:"excerpt"`
	ExpiresAt      time.Time  `json:"expires_at"`
	LastAccessedAt *time.Time `json:"last_accessed_at"`
	ViewCount      int        `json:"view_count"`
	NoteActive     bool       `json:"note_active"`
}

type createdShare struct {
	Link shareLinkJSON `json:"link"`
	URL  string        `json:"url"`
}

// token extracts the secret from the address a person would send: everything after the fragment mark.
func (c createdShare) token(t *testing.T) string {
	t.Helper()
	_, tok, ok := strings.Cut(c.URL, "/s#")
	if !ok {
		t.Fatalf("share address %q has no fragment token", c.URL)
	}
	return tok
}

func (u *appUser) share(note, expiresIn string) createdShare {
	u.t.Helper()
	var c createdShare
	u.post("/api/v1/notes/"+note+"/share-links", map[string]any{"expires_in": expiresIn}, 201, &c)
	return c
}

func (u *appUser) shareLinks(query string) (items []shareLinkJSON) {
	u.t.Helper()
	var out struct {
		Items []shareLinkJSON `json:"items"`
	}
	u.get("/api/v1/share-links"+query, &out)
	return out.Items
}

// publicDo calls the public share API the way the share page does: token in a header, no cookies.
func (s *stack) publicDo(token, method, path string, headers map[string]string) response {
	s.t.Helper()
	req, _ := http.NewRequest(method, s.public.URL+path, nil)
	if token != "" {
		req.Header.Set("X-Share-Token", token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return response{Status: res.StatusCode, Header: res.Header, Body: b}
}

type sharedNoteJSON struct {
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Parts     []struct {
		Kind       string  `json:"kind"`
		Text       *string `json:"text"`
		Attachment *struct {
			ID       string `json:"id"`
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
		} `json:"attachment"`
	} `json:"parts"`
}

// noteWithFile creates a note with a text part and one attachment and returns the note and the file bytes.
func (u *appUser) noteWithFile(text, filename, mediaType string, data []byte) (noteJSON, string) {
	u.t.Helper()
	id := uuid.NewString()
	if res := u.upload(id, filename, mediaType, data); res.Status != 201 {
		u.t.Fatalf("upload: %d %s", res.Status, res.Body)
	}
	var n noteJSON
	u.post("/api/v1/notes", map[string]any{"parts": []map[string]any{{"type": "text", "text": text}, {"type": "attachment", "attachment_id": id}}}, 201, &n)
	return n, id
}

// A link opens the note's current content for anyone holding it, shows nothing else, follows edits,
// counts views, and stops the moment it is revoked or the note goes to the Trash (CORE-SH1, CORE-SH3,
// CORE-SH4, CORE-SH5, CORE-SH12, SEC-SHR-1, SEC-SHR-4).
func TestShareLinkLifecycle(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	work := u.page("Work")
	cat := u.category(work, "Todo")
	file := randomBytes(700_000)
	n, att := u.noteWithFile("- [ ] pack the bags\n- [x] book flights", "tickets.pdf", "application/pdf", file)
	u.post("/api/v1/notes/"+n.ID+"/move", map[string]any{"category_id": cat}, 200, nil)

	created := u.share(n.ID, "7d")
	token := created.token(t)
	if len(token) != 32 || !strings.HasPrefix(created.URL, "https://share.example.net/s#") {
		t.Fatalf("address: %s", created.URL)
	}
	if want := time.Now().Add(7 * 24 * time.Hour); created.Link.ExpiresAt.Sub(want).Abs() > time.Minute {
		t.Fatalf("expiry %v", created.Link.ExpiresAt)
	}

	res := s.publicDo(token, "GET", "/api/public/v1/share", nil)
	if res.Status != 200 {
		t.Fatalf("open: %d %s", res.Status, res.Body)
	}
	var shared sharedNoteJSON
	res.JSON(t, &shared)
	if len(shared.Parts) != 2 || *shared.Parts[0].Text != "- [ ] pack the bags\n- [x] book flights" || shared.Parts[1].Attachment == nil ||
		shared.Parts[1].Attachment.Filename != "tickets.pdf" || shared.Parts[1].Attachment.Size != int64(len(file)) {
		t.Fatalf("shared note: %s", res.Body)
	}
	// It shows the note and nothing else: no page, category, owner, source, reminders or history (SEC-SHR-4).
	var raw map[string]any
	_ = json.Unmarshal(res.Body, &raw)
	if len(raw) != 3 {
		t.Fatalf("top-level fields: %v", raw)
	}
	for _, leak := range []string{"alice", "Work", "Todo", cat, work, u.userID(), "category", "page", "source", "history", "reminder"} {
		if strings.Contains(string(res.Body), leak) {
			t.Fatalf("the shared note leaks %q: %s", leak, res.Body)
		}
	}

	// The current content is shown: an edit made after the link was created is visible (CORE-SH4).
	u.c.do("PATCH", "/api/v1/notes/"+n.ID+"/parts/"+n.Parts[0].ID, map[string]any{"text": "- [x] pack the bags"})
	s.publicDo(token, "GET", "/api/public/v1/share", nil).JSON(t, &shared)
	if *shared.Parts[0].Text != "- [x] pack the bags" {
		t.Fatalf("stale content: %q", *shared.Parts[0].Text)
	}

	// The file comes with the token itself, with Range, never cached (CORE-SH7, CORE-SH8).
	dl := s.publicDo(token, "GET", "/api/public/v1/share/attachments/"+att, nil)
	if dl.Status != 200 || !bytes.Equal(dl.Body, file) || dl.Header.Get("Cache-Control") != "no-store" || dl.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d, %d bytes, %v", dl.Status, len(dl.Body), dl.Header)
	}
	part := s.publicDo(token, "GET", "/api/public/v1/share/attachments/"+att, map[string]string{"Range": "bytes=100-199"})
	if part.Status != 206 || !bytes.Equal(part.Body, file[100:200]) {
		t.Fatalf("range: %d", part.Status)
	}

	// The owner sees usage (CORE-SH3).
	links := u.shareLinks("")
	if len(links) != 1 || links[0].ViewCount != 2 || links[0].LastAccessedAt == nil || links[0].Excerpt != "- [x] pack the bags" || !links[0].NoteActive {
		t.Fatalf("links: %+v", links)
	}
	if got := u.shareLinks("?note_id=" + n.ID); len(got) != 1 {
		t.Fatalf("per note: %+v", got)
	}
	if got := u.shareLinks("?note_id=" + uuid.NewString()); len(got) != 0 {
		t.Fatalf("other note: %+v", got)
	}

	// Moving the note around does not matter (CORE-SH5).
	u.post("/api/v1/notes/"+n.ID+"/move", map[string]any{"category_id": nil}, 200, nil)
	if s.publicDo(token, "GET", "/api/public/v1/share", nil).Status != 200 {
		t.Fatal("moving a note must not break its link")
	}

	// Dismissing switches the link off at once; restoring switches it on again until its own expiry.
	u.post("/api/v1/notes/"+n.ID+"/dismiss", nil, 200, nil)
	if s.publicDo(token, "GET", "/api/public/v1/share", nil).Status != 404 || s.publicDo(token, "GET", "/api/public/v1/share/attachments/"+att, nil).Status != 404 {
		t.Fatal("a note in the Trash must not be reachable through its link")
	}
	if got := u.shareLinks(""); len(got) != 1 || got[0].NoteActive {
		t.Fatalf("the owner must see the link as not working: %+v", got)
	}
	u.post("/api/v1/notes/"+n.ID+"/restore", nil, 200, nil)
	if s.publicDo(token, "GET", "/api/public/v1/share", nil).Status != 200 {
		t.Fatal("restoring must re-enable the link")
	}

	// Revoking works at once, and is audited without content or token (CORE-SH12).
	if res := u.c.do("DELETE", "/api/v1/share-links/"+created.Link.ID, nil); res.Status != 204 {
		t.Fatalf("revoke: %d", res.Status)
	}
	if s.publicDo(token, "GET", "/api/public/v1/share", nil).Status != 404 || len(u.shareLinks("")) != 0 {
		t.Fatal("a revoked link must stop working and leave the list")
	}
	if res := u.c.do("DELETE", "/api/v1/share-links/"+created.Link.ID, nil); res.Status != 404 {
		t.Fatalf("revoking twice: %d", res.Status)
	}
	if s.count(`select count(*) from audit_log where action in ('share.created','share.revoked') and detail::text not like '%'||$1||'%'`, token) != 2 ||
		s.count(`select count(*) from audit_log where detail::text like '%'||$1||'%' or detail::text like '%tickets%' or detail::text like '%pack the bags%'`, token) != 0 {
		t.Fatal("the audit log must record creation and revocation without the token or any content")
	}
}

func (u *appUser) userID() string {
	var me struct {
		ID string `json:"id"`
	}
	u.get("/api/v1/me", &me)
	return me.ID
}

// Whatever is wrong with a link, the answer is the same, so a token cannot be probed (CORE-SH6, SEC-SHR-3).
func TestEveryDeadLinkAnswersTheSame(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	note := func(text string) (string, string) {
		n := u.note("", text, nil)
		return n.ID, u.share(n.ID, "1d").token(t)
	}
	_, expired := note("expired")
	revokedNote, revoked := note("revoked")
	_, dismissedTok := note("dismissed")
	_, live := note("live")

	if _, err := s.db.Admin.Exec(t.Context(), `update share_links set expires_at = now() - interval '1 minute' where token_hash = $1`, sha(expired)); err != nil {
		t.Fatal(err)
	}
	links := u.shareLinks("")
	for _, l := range links {
		if l.NoteID == revokedNote {
			u.c.do("DELETE", "/api/v1/share-links/"+l.ID, nil)
		}
	}
	for _, l := range links {
		if l.Excerpt == "dismissed" {
			u.post("/api/v1/notes/"+l.NoteID+"/dismiss", nil, 200, nil)
		}
	}
	bob := s.appUser("bob")
	bn := bob.note("", "bob's note", nil)
	bobTok := bob.share(bn.ID, "1d").token(t)
	if _, err := s.db.Admin.Exec(t.Context(), `update users set status = 'disabled' where username = 'bob'`); err != nil {
		t.Fatal(err)
	}

	unknown := s.publicDo("A"+strings.Repeat("b", 31), "GET", "/api/public/v1/share", nil)
	normalise := func(r response) string { // the request id is per request by design
		var m map[string]any
		_ = json.Unmarshal(r.Body, &m)
		delete(m, "request_id")
		b, _ := json.Marshal(m)
		return itoa(r.Status) + string(b)
	}
	want := normalise(unknown)
	if unknown.Status != 404 {
		t.Fatalf("unknown token: %d %s", unknown.Status, unknown.Body)
	}
	for name, tok := range map[string]string{"expired": expired, "revoked": revoked, "dismissed": dismissedTok, "owner disabled": bobTok, "malformed": "short", "empty": "", "wrong alphabet": strings.Repeat("!", 32)} {
		for _, path := range []string{"/api/public/v1/share", "/api/public/v1/share/attachments/" + uuid.NewString()} {
			if got := normalise(s.publicDo(tok, "GET", path, nil)); got != want {
				t.Errorf("%s on %s: %s, want %s", name, path, got, want)
			}
		}
	}
	if s.publicDo(live, "GET", "/api/public/v1/share", nil).Status != 200 {
		t.Fatal("the control link must work")
	}
	// Sharing switched off by the operator stops every link (CORE-SH11).
	s.svc.Shares.Cfg.Enabled = false
	if got := normalise(s.publicDo(live, "GET", "/api/public/v1/share", nil)); got != want {
		t.Fatalf("sharing disabled: %s", got)
	}
	s.svc.Shares.Cfg.Enabled = true
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func sha(token string) []byte { h := sha256.Sum256([]byte(token)); return h[:] }

// Links can only be made for one's own active notes, only with a chosen expiry within the operator's
// maximum, and only so many, so fast; the rules hold for hand-made requests too (CORE-SH1, CORE-SH2,
// CORE-SH11, SEC-SHR-2, SEC-SHR-9, SEC-SHR-11).
func TestShareCreationRules(t *testing.T) {
	s := newStackWith(t, nil)
	alice, bob := s.appUser("alice"), s.appUser("bob")
	n := alice.note("", "mine", nil)
	create := func(u *appUser, note string, body any) response {
		return u.c.do("POST", "/api/v1/notes/"+note+"/share-links", body)
	}

	for name, body := range map[string]any{"no expiry": map[string]any{}, "forever": map[string]any{"expires_in": "forever"}, "number": map[string]any{"expires_in": 999999},
		"empty": map[string]any{"expires_in": ""}, "no body": nil} {
		if res := create(alice, n.ID, body); res.Status != 400 {
			t.Errorf("%s: %d %s", name, res.Status, res.Body)
		}
	}
	// Somebody else's note is not found, and creates nothing (SEC-SHR-9).
	if res := create(bob, n.ID, map[string]any{"expires_in": "1d"}); res.Status != 404 {
		t.Fatalf("foreign note: %d", res.Status)
	}
	if s.count(`select count(*) from share_links`) != 0 {
		t.Fatal("a failed request created a link")
	}
	// The operator's maximum lifetime binds (SEC-SHR-2).
	s.svc.Shares.Cfg.MaxLifetime = 2 * 24 * time.Hour
	if res := create(alice, n.ID, map[string]any{"expires_in": "7d"}); res.Status != 400 {
		t.Fatalf("7d over a 2d maximum: %d", res.Status)
	}
	if res := create(alice, n.ID, map[string]any{"expires_in": "1d"}); res.Status != 201 {
		t.Fatalf("1d: %d %s", res.Status, res.Body)
	}
	s.svc.Shares.Cfg.MaxLifetime = 30 * 24 * time.Hour

	// A note in the Trash cannot be shared.
	gone := alice.note("", "trashed", nil)
	alice.post("/api/v1/notes/"+gone.ID+"/dismiss", nil, 200, nil)
	if res := create(alice, gone.ID, map[string]any{"expires_in": "1d"}); res.Status != 409 {
		t.Fatalf("dismissed note: %d", res.Status)
	}

	// The number of live links and the pace of creation are capped (SEC-SHR-11).
	s.svc.Shares.Cfg.MaxActive = 3
	for range 2 {
		alice.share(n.ID, "1h")
	}
	if res := create(alice, n.ID, map[string]any{"expires_in": "1h"}); res.Status != 429 || res.Code() != "share_limit" {
		t.Fatalf("active cap: %d %s", res.Status, res.Body)
	}
	s.svc.Shares.Cfg.MaxActive = 100
	s.svc.Shares.Cfg.CreatedPerHour = 3
	if res := create(alice, n.ID, map[string]any{"expires_in": "1h"}); res.Status != 429 {
		t.Fatalf("hourly cap: %d %s", res.Status, res.Body)
	}
	s.svc.Shares.Cfg.CreatedPerHour = 30

	// Switching sharing off refuses new links (CORE-SH11).
	s.svc.Shares.Cfg.Enabled = false
	if res := create(alice, n.ID, map[string]any{"expires_in": "1h"}); res.Status != 403 || res.Code() != "sharing_disabled" {
		t.Fatalf("disabled: %d %s", res.Status, res.Body)
	}
	var list struct {
		Enabled bool `json:"enabled"`
	}
	alice.get("/api/v1/share-links", &list)
	if list.Enabled {
		t.Fatal("the list must say sharing is off, so the app can hide the action")
	}
	s.svc.Shares.Cfg.Enabled = true

	// Revoke all ends every link of the user, and only the user's (CORE-SH14).
	bn := bob.note("", "bobs", nil)
	bobLink := bob.share(bn.ID, "1d")
	var out struct {
		Revoked int `json:"revoked"`
	}
	alice.post("/api/v1/share-links/revoke-all", nil, 200, &out)
	if out.Revoked != 3 || len(alice.shareLinks("")) != 0 || len(bob.shareLinks("")) != 1 {
		t.Fatalf("revoke all: %+v", out)
	}
	if s.publicDo(bobLink.token(t), "GET", "/api/public/v1/share", nil).Status != 200 {
		t.Fatal("revoking Alice's links must not touch Bob's")
	}
	// Nobody can list or revoke another person's links.
	if res := bob.c.do("DELETE", "/api/v1/share-links/"+bobLink.Link.ID, nil); res.Status != 204 {
		t.Fatalf("own revoke: %d", res.Status)
	}
}

// Tokens are long, random and stored only as hashes; the same content gets a new token every time (CORE-SH6, SEC-SHR-3).
func TestTokensAreRandomAndOnlyHashesAreStored(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n := u.note("", "hello", nil)
	seen := map[string]bool{}
	for range 5 {
		tok := u.share(n.ID, "1d").token(t)
		if seen[tok] {
			t.Fatal("token repeated")
		}
		seen[tok] = true
		if len(tok) != 32 {
			t.Fatalf("token length %d", len(tok))
		}
		if s.count(`select count(*) from share_links where token_hash = $1`, sha(tok)) != 1 {
			t.Fatal("the stored value must be the SHA-256 of the token")
		}
		if s.count(`select count(*) from share_links where encode(token_hash, 'hex') = $1 or encode(token_hash, 'escape') = $1`, tok) != 0 || hex.EncodeToString(sha(tok)) == tok {
			t.Fatal("the token itself is stored")
		}
	}
	var dump []byte
	rows, err := s.db.Admin.Query(t.Context(), `select to_jsonb(l)::text from share_links l`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var j string
		_ = rows.Scan(&j)
		dump = append(dump, j...)
	}
	rows.Close()
	for tok := range seen {
		if bytes.Contains(dump, []byte(tok)) {
			t.Fatal("a token appears in the database")
		}
	}
}

// The public listener serves only the share routes, read-only, with no cookies in either direction,
// and the app's listener does not serve them (CORE-SH7, CORE-SH8, CORE-SH10, SEC-SHR-5, SEC-SHR-6).
func TestPublicListenerIsIsolatedAndReadOnly(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n, att := u.noteWithFile("hello", "evil.html", "text/html", []byte("<script>alert(1)</script>"))
	token := u.share(n.ID, "1d").token(t)

	// Only the share routes exist there: the application's API and the bot API do not.
	for _, path := range []string{"/api/v1/notes", "/api/v1/me", "/api/v1/notes/" + n.ID, "/api/v1/attachments/" + att, "/api/v1/share-links", "/bot/v1/events", "/api/v1/events"} {
		if res := s.publicDo(token, "GET", path, nil); res.Status != 404 {
			t.Errorf("public listener served %s: %d", path, res.Status)
		}
	}
	// And the app's listener does not serve the share routes.
	req, _ := http.NewRequest("GET", s.user.URL+"/api/public/v1/share", nil)
	req.Header.Set("X-Share-Token", token)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 404 {
		t.Fatalf("app listener served a public route: %v %v", err, res)
	}
	// Read-only: nothing but GET.
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if res := s.publicDo(token, m, "/api/public/v1/share", nil); res.Status != 405 {
			t.Errorf("%s: %d", m, res.Status)
		}
	}
	// A session cookie is ignored and no cookie is ever set.
	res := s.publicDo(token, "GET", "/api/public/v1/share", map[string]string{"Cookie": "__Host-nka=" + u.c.cookies["__Host-nka"]})
	if res.Status != 200 || res.Header.Get("Set-Cookie") != "" {
		t.Fatalf("cookies: %d %v", res.Status, res.Header)
	}
	if bad := s.publicDo("", "GET", "/api/public/v1/share", map[string]string{"Cookie": "__Host-nka=" + u.c.cookies["__Host-nka"], "Authorization": "Bearer x"}); bad.Status != 404 {
		t.Fatalf("a session must not stand in for a token: %d", bad.Status)
	}
	// The headers that keep a link private (SEC-SHR-6).
	h := res.Header
	if h.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(h.Get("X-Robots-Tag"), "noindex") || h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", h)
	}
	// A file that could run in a browser is a download, never a page (SEC-CNT-3, SEC-SHR-5).
	dl := s.publicDo(token, "GET", "/api/public/v1/share/attachments/"+att, nil)
	if dl.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(dl.Header.Get("Content-Disposition"), "attachment") ||
		dl.Header.Get("Content-Security-Policy") != "sandbox" || dl.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("download headers: %v", dl.Header)
	}
	// Only the shared note's files: another of the owner's notes, and another user's file, are not reachable (SEC-SHR-4).
	_, otherAtt := u.noteWithFile("other", "secret.txt", "text/plain", []byte("secret"))
	bob := s.appUser("bob")
	_, bobAtt := bob.noteWithFile("bobs", "bob.txt", "text/plain", []byte("bob secret"))
	for name, id := range map[string]string{"another note of the owner": otherAtt, "another user": bobAtt, "made up": uuid.NewString()} {
		if r := s.publicDo(token, "GET", "/api/public/v1/share/attachments/"+id, nil); r.Status != 404 {
			t.Errorf("%s: %d", name, r.Status)
		}
	}
	for _, sneaky := range []string{"/api/public/v1/share/attachments/../../../v1/notes", "/api/public/v1/share/attachments/" + att + "/../" + otherAtt, "/api/public/v1/share/attachments/%2e%2e%2f" + otherAtt} {
		if r := s.publicDo(token, "GET", sneaky, nil); r.Status == 200 && bytes.Contains(r.Body, []byte("secret")) {
			t.Errorf("path trick %s reached another file", sneaky)
		}
	}
}

// One caller or one link cannot consume the server: requests per address, requests per link and
// bytes per link are all limited (CORE-SH9, SEC-SHR-8).
func TestPublicEndpointsAreRateLimited(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n, att := u.noteWithFile("hello", "big.bin", "application/octet-stream", randomBytes(10<<20))
	token := u.share(n.ID, "1d").token(t)

	// Downloads count against the link's bandwidth: 100 MiB burst, so the eleventh 10 MiB file is refused.
	refused := false
	for i := range 14 {
		res := s.publicDo(token, "GET", "/api/public/v1/share/attachments/"+att, nil)
		if res.Status == 429 {
			refused = true
			if res.Header.Get("Retry-After") == "" {
				t.Fatal("a refusal must say when to come back")
			}
			if i < 9 {
				t.Fatalf("refused too early (download %d)", i)
			}
			break
		}
	}
	if !refused {
		t.Fatal("the bandwidth ceiling never applied")
	}

	// Unknown tokens from one address are refused after a burst, so guessing is slow as well as hopeless.
	limited := false
	for range 200 {
		if s.publicDo(strings.Repeat("x", 32), "GET", "/api/public/v1/share", nil).Status == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("the per-address limit never applied")
	}
}

// Expired links are deleted a week later, not before (CORE-SH2).
func TestExpiredLinksArePurgedAfterAGrace(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n := u.note("", "hello", nil)
	old, recent, live := u.share(n.ID, "1h"), u.share(n.ID, "1h"), u.share(n.ID, "1d")
	for id, age := range map[string]string{old.Link.ID: "8 days", recent.Link.ID: "1 day"} {
		if _, err := s.db.Admin.Exec(t.Context(), `update share_links set expires_at = now() - $2::interval where id = $1`, id, age); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.svc.Shares.Purge(t.Context(), 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if s.count(`select count(*) from share_links`) != 2 || s.count(`select count(*) from share_links where id = $1`, old.Link.ID) != 0 || s.count(`select count(*) from share_links where id = $1`, live.Link.ID) != 1 {
		t.Fatal("only the link expired more than a week ago may go")
	}
}

var _ = store.ErrNotFound
