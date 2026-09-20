//go:build integration

package server_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/blobs"
	"github.com/Niboor/notekeeper/core/internal/config"
)

// upload sends a file the way the web app does and returns the response.
func (u *appUser) upload(id, name, mediaType string, data []byte) response {
	u.t.Helper()
	return u.c.doRaw("PUT", "/api/v1/attachments/"+id, bytes.NewReader(data), int64(len(data)), map[string]string{
		"X-Filename": url.PathEscape(name), "X-Media-Type": mediaType, "Content-Type": "application/octet-stream"})
}

// doRaw sends a raw body with an explicit length (negative: chunked).
func (c *client) doRaw(method, path string, body io.Reader, length int64, headers map[string]string) response {
	c.s.t.Helper()
	req, _ := http.NewRequest(method, c.s.user.URL+path, body)
	req.ContentLength = length
	req.Header.Set("X-Notekeeper-Client", "web")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for k, v := range c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.s.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return response{Status: res.StatusCode, Header: res.Header, Body: b}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

type storage struct{ used, reserved int64 }

func (s *stack) storageOf(user string) storage {
	s.t.Helper()
	var st storage
	err := s.db.Admin.QueryRow(context.Background(),
		`select coalesce(used_bytes,0), coalesce(reserved_bytes,0) from users u left join user_storage s on s.user_id = u.id where u.username = $1`, user).Scan(&st.used, &st.reserved)
	if err != nil {
		s.t.Fatal(err)
	}
	return st
}

func (s *stack) count(query string, args ...any) int {
	s.t.Helper()
	var n int
	if err := s.db.Admin.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

// attachNote creates an Inbox note holding the attachment and returns it.
func (u *appUser) attachNote(attachmentID string) noteJSON {
	var n noteJSON
	u.post("/api/v1/notes", map[string]any{"parts": []map[string]any{{"type": "attachment", "attachment_id": attachmentID}}}, 201, &n)
	return n
}

// Attachments are stored by Core and served only to their owner, exactly as uploaded (CORE-A1, CORE-A4, CORE-A6).
func TestUploadLinkAndDownload(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	png := append([]byte("\x89PNG\r\n\x1a\n"), randomBytes(700_000)...) // three chunks
	id := uuid.NewString()

	res := u.upload(id, "ticket photo.png", "image/png; charset=binary", png)
	if res.Status != 201 {
		t.Fatalf("upload: %d %s", res.Status, res.Body)
	}
	var ref struct {
		ID, Filename string
		MediaType    string `json:"media_type"`
		Size         int64
	}
	res.JSON(t, &ref)
	if ref.Filename != "ticket photo.png" || ref.MediaType != "image/png" || ref.Size != int64(len(png)) {
		t.Fatalf("attachment: %+v", ref)
	}
	// Idempotent on the id (CORE-S5): repeating it stores nothing new.
	if res := u.upload(id, "ticket photo.png", "image/png", png); res.Status != 201 {
		t.Fatalf("repeat: %d %s", res.Status, res.Body)
	}
	if n := s.count(`select count(*) from attachments`); n != 1 {
		t.Fatalf("%d attachments after a repeated upload", n)
	}

	// It is not visible in any note until a part references it, and can be linked exactly once.
	note := u.attachNote(id)
	if note.Parts[0].Kind != "attachment" {
		t.Fatalf("part: %+v", note.Parts[0])
	}
	u.post("/api/v1/notes", map[string]any{"parts": []map[string]any{{"type": "attachment", "attachment_id": id}}}, 409, nil)

	dl := u.c.do("GET", "/api/v1/attachments/"+id, nil)
	if dl.Status != 200 || !bytes.Equal(dl.Body, png) {
		t.Fatalf("download: %d, %d bytes, identical=%v", dl.Status, len(dl.Body), bytes.Equal(dl.Body, png))
	}
	for h, want := range map[string]string{
		"Content-Type": "image/png", "X-Content-Type-Options": "nosniff", "Content-Security-Policy": "sandbox", "Cache-Control": "private, no-cache",
	} {
		if got := dl.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	if cd := dl.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "inline;") || !strings.Contains(cd, "filename*=UTF-8''ticket%20photo.png") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	etag := dl.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	// Conditional and range requests.
	if r := u.c.doWith("GET", "/api/v1/attachments/"+id, nil, func(r *http.Request) { r.Header.Set("If-None-Match", etag) }); r.Status != 304 {
		t.Fatalf("If-None-Match: %d", r.Status)
	}
	part := u.c.doWith("GET", "/api/v1/attachments/"+id, nil, func(r *http.Request) { r.Header.Set("Range", "bytes=262100-262300") })
	if part.Status != 206 || !bytes.Equal(part.Body, png[262100:262301]) { // a range across a chunk boundary
		t.Fatalf("range: %d, %d bytes", part.Status, len(part.Body))
	}
	if r := u.c.doWith("GET", "/api/v1/attachments/"+id, nil, func(r *http.Request) { r.Header.Set("Range", "bytes=99999999-") }); r.Status != 416 {
		t.Fatalf("unsatisfiable range: %d", r.Status)
	}
	// Many ranges in one request are refused: each would cost a database read (SR-009).
	many := strings.TrimSuffix(strings.Repeat("0-10,", 400), ",")
	if r := u.c.doWith("GET", "/api/v1/attachments/"+id, nil, func(r *http.Request) { r.Header.Set("Range", "bytes="+many) }); r.Status != 416 || !strings.HasPrefix(r.Header.Get("Content-Range"), "bytes */") {
		t.Fatalf("multi-range: %d %v", r.Status, r.Header)
	}
	// The note shows the attachment.
	var got noteJSONWithAtt
	u.get("/api/v1/notes/"+note.ID, &got)
	if got.Parts[0].Attachment == nil || got.Parts[0].Attachment.Filename != "ticket photo.png" || got.Parts[0].Attachment.Size != int64(len(png)) {
		t.Fatalf("note part attachment: %+v", got.Parts[0])
	}
}

type noteJSONWithAtt struct {
	Parts []struct {
		Attachment *struct {
			ID, Filename string
			Size         int64
		} `json:"attachment"`
	} `json:"parts"`
}

// Files that could run in the browser are never served inline (SEC-CNT-3); filenames cannot
// inject headers or paths (SEC-CNT-4).
func TestDangerousFilesAreDownloadsAndNamesAreCleaned(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	for _, tc := range []struct{ name, mediaType string }{
		{"evil.html", "text/html"}, {"logo.svg", "image/svg+xml"}, {"x.js", "application/javascript"}, {"weird", "not a media type"},
	} {
		id := uuid.NewString()
		if res := u.upload(id, tc.name, tc.mediaType, []byte("<script>alert(1)</script>")); res.Status != 201 {
			t.Fatal(res.Status, string(res.Body))
		}
		u.attachNote(id)
		dl := u.c.do("GET", "/api/v1/attachments/"+id, nil)
		if dl.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(dl.Header.Get("Content-Disposition"), "attachment;") {
			t.Errorf("%s served as %q / %q", tc.name, dl.Header.Get("Content-Type"), dl.Header.Get("Content-Disposition"))
		}
		if dl.Header.Get("X-Content-Type-Options") != "nosniff" || dl.Header.Get("Content-Security-Policy") != "sandbox" {
			t.Errorf("%s lacks the protective headers", tc.name)
		}
	}
	id := uuid.NewString()
	nasty := "../../etc/passwd\r\nSet-Cookie: pwned=1\x00.png"
	if res := u.upload(id, nasty, "image/png", []byte("x")); res.Status != 201 {
		t.Fatal(res.Status, string(res.Body))
	}
	u.attachNote(id)
	dl := u.c.do("GET", "/api/v1/attachments/"+id, nil)
	if dl.Header.Get("Set-Cookie") != "" || strings.ContainsAny(dl.Header.Get("Content-Disposition"), "\r\n") {
		t.Fatalf("header injection through the filename: %q", dl.Header)
	}
	var name string
	_ = s.db.Admin.QueryRow(t.Context(), `select filename from attachments where id = $1`, id).Scan(&name)
	if strings.ContainsAny(name, "/\\\r\n\x00") || strings.HasPrefix(name, ".") {
		t.Fatalf("stored filename %q", name)
	}
}

// The maximum attachment size is enforced before the body is read, with clear errors (CORE-A3).
func TestUploadLimitsAndBadRequests(t *testing.T) {
	s := newStackWith(t, func(c *config.Config) { c.MaxAttachmentBytes = 1 << 20 })
	u := s.appUser("alice")
	// Chunked bodies (no Content-Length) are refused: the quota is reserved against the declared length.
	if res := u.c.doRaw("PUT", "/api/v1/attachments/"+uuid.NewString(), io.NopCloser(strings.NewReader("data")), -1, map[string]string{"X-Filename": "a", "Content-Type": "application/octet-stream"}); res.Status != 411 {
		t.Fatalf("no length: %d", res.Status)
	}
	// Above the maximum: refused up front, without reading the body (CORE-A3).
	if res := u.upload(uuid.NewString(), "big", "application/octet-stream", make([]byte, 1<<20+1)); res.Status != 413 || res.Code() != "too_large" {
		t.Fatalf("too large: %d %s", res.Status, res.Body)
	}
	if res := u.upload(uuid.NewString(), "empty", "application/octet-stream", nil); res.Status != 400 {
		t.Fatalf("empty: %d", res.Status)
	}
	// Non-UUID ids are rejected by the router's parameter binding.
	if res := u.c.doRaw("PUT", "/api/v1/attachments/not-a-uuid", strings.NewReader("x"), 1, map[string]string{"X-Filename": "a", "Content-Type": "application/octet-stream"}); res.Status != 400 {
		t.Fatalf("bad id: %d", res.Status)
	}
	if st := s.storageOf("alice"); st.used != 0 || st.reserved != 0 {
		t.Fatalf("failed uploads left storage state behind: %+v", st)
	}
}

// A connection that dies mid-upload, or a body that disagrees with its Content-Length, leaves no
// blob behind and gives the reserved space back (docs/design/01 section 6.2).
func TestBrokenUploadsReleaseTheirReservation(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	host := strings.TrimPrefix(s.user.URL, "http://")
	send := func(declared int, bodyLen int) {
		conn, err := net.Dial("tcp", host)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "PUT /api/v1/attachments/%s HTTP/1.1\r\nHost: %s\r\nX-Notekeeper-Client: web\r\nSec-Fetch-Site: same-origin\r\nCookie: __Host-nka=%s\r\nX-Filename: f\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n",
			uuid.NewString(), host, u.c.cookies["__Host-nka"], declared)
		_, _ = conn.Write(make([]byte, bodyLen))
		_ = conn.Close() // hang up before the promised body is complete
	}
	send(2_000_000, 600_000)
	send(500_000, 10)
	deadline := time.Now().Add(15 * time.Second)
	time.Sleep(500 * time.Millisecond) // the server may not have started on the hung-up requests yet
	clean := 0
	for {
		st := s.storageOf("alice")
		if st.reserved == 0 && s.count(`select count(*) from blobs`) == 0 {
			if clean++; clean >= 3 { // clean for a while, not just before the requests were looked at
				break
			}
		} else {
			clean = 0
		}
		if time.Now().After(deadline) {
			t.Fatalf("storage after broken uploads: %+v, blobs=%d", st, s.count(`select count(*) from blobs`))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := s.count(`select count(*) from blob_chunks`); n != 0 {
		t.Fatalf("%d orphaned chunks", n)
	}
	// Sending MORE than declared is refused too (the extra byte is noticed).
	svc := s.svc.Blobs
	uid := s.lookupUser("alice")
	if _, err := svc.Upload(t.Context(), uid, uuid.New(), "f", "x/y", 5, strings.NewReader("123456789")); err != blobs.ErrLength {
		t.Fatalf("overrun: %v", err)
	}
	if st := s.storageOf("alice"); st.used != 0 || st.reserved != 0 {
		t.Fatalf("after overrun: %+v", st)
	}
}

// Parallel uploads cannot exceed the quota, however they interleave (SEC-CNT-6, SEC-API-7).
func TestQuotaCannotBeExceededByParallelUploads(t *testing.T) {
	s := newStackWith(t, func(c *config.Config) { c.MaxConcurrentUpload = 100 }) // the point is the quota, not the slots
	u := s.appUser("alice")
	uid := s.lookupUser("alice")
	quota := int64(10 << 20)
	if err := s.svc.Accounts.SetQuota(t.Context(), storeActor(), uid, &quota); err != nil {
		t.Fatal(err)
	}
	var ok, refused atomic.Int32
	var wg sync.WaitGroup
	for range 8 { // 8 x 3 MiB against a 10 MiB quota: at most three fit
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch res := u.upload(uuid.NewString(), "f", "application/octet-stream", randomBytes(3<<20)); {
			case res.Status == 201:
				ok.Add(1)
			case res.Status == 413 && res.Code() == "quota_exceeded":
				refused.Add(1)
			default:
				t.Errorf("unexpected: %d %s", res.Status, res.Body)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 3 || refused.Load() != 5 {
		t.Fatalf("%d uploads succeeded, %d refused; exactly 3 fit the quota", ok.Load(), refused.Load())
	}
	st := s.storageOf("alice")
	if st.used != 3*(3<<20) || st.reserved != 0 || st.used > quota {
		t.Fatalf("storage: %+v", st)
	}
}

// Identical content is stored once per user and never shared between users (CORE-A5, SEC-ISO-9);
// deleting the last reference frees the space (CORE-A8, SEC-DATA-5).
func TestDeduplicationAndCleanup(t *testing.T) {
	s := newStack(t)
	alice, bob := s.appUser("alice"), s.appUser("bob")
	data := randomBytes(400_000)
	a1, a2, b1 := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, x := range []struct {
		u  *appUser
		id string
	}{{alice, a1}, {alice, a2}, {bob, b1}} {
		if res := x.u.upload(x.id, "same.bin", "application/octet-stream", data); res.Status != 201 {
			t.Fatal(res.Status, string(res.Body))
		}
	}
	if n := s.count(`select count(*) from blobs`); n != 2 { // one for alice (shared by two attachments), one for bob
		t.Fatalf("%d blobs; content must be deduplicated per user only", n)
	}
	if st := s.storageOf("alice"); st.used != int64(len(data)) {
		t.Fatalf("alice is charged for the file once: %+v", st)
	}
	n1, n2 := alice.attachNote(a1), alice.attachNote(a2)
	alice.post("/api/v1/notes/"+n1.ID+"/dismiss", nil, 200, nil)
	if res := alice.c.do("DELETE", "/api/v1/notes/"+n1.ID, nil); res.Status != 204 {
		t.Fatal(res.Status)
	}
	if n := s.count(`select count(*) from blobs`); n != 2 || s.storageOf("alice").used == 0 {
		t.Fatal("the shared blob must survive while another attachment uses it")
	}
	alice.post("/api/v1/notes/"+n2.ID+"/dismiss", nil, 200, nil)
	alice.c.do("DELETE", "/api/v1/notes/"+n2.ID, nil)
	if n := s.count(`select count(*) from blobs where user_id = $1`, s.lookupUser("alice")); n != 0 {
		t.Fatalf("alice still has %d blobs", n)
	}
	if st := s.storageOf("alice"); st.used != 0 || s.count(`select count(*) from attachments where user_id = $1`, s.lookupUser("alice")) != 0 {
		t.Fatalf("storage after deleting everything: %+v", st)
	}
	// Bob's copy is unaffected, and alice cannot reach it.
	if res := alice.c.do("GET", "/api/v1/attachments/"+b1, nil); res.Status != 404 {
		t.Fatalf("foreign attachment: %d", res.Status)
	}
	bob.attachNote(b1)
	if res := bob.c.do("GET", "/api/v1/attachments/"+b1, nil); res.Status != 200 || !bytes.Equal(res.Body, data) {
		t.Fatalf("bob's file: %d", res.Status)
	}
	// Linking someone else's attachment into one's own note looks like a missing attachment (SEC-ISO-4).
	alice.post("/api/v1/notes", map[string]any{"parts": []map[string]any{{"type": "attachment", "attachment_id": b1}}}, 404, nil)
}

func TestRemovingAnAttachmentPartFreesItsFile(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	id := uuid.NewString()
	u.upload(id, "a.bin", "application/octet-stream", randomBytes(100_000))
	n := u.attachNote(id)
	u.post("/api/v1/notes/"+n.ID+"/parts", map[string]any{"type": "text", "text": "caption"}, 201, nil)
	if res := u.c.do("DELETE", "/api/v1/notes/"+n.ID+"/parts/"+n.Parts[0].ID, nil); res.Status != 200 {
		t.Fatal(res.Status, string(res.Body))
	}
	if s.count(`select count(*) from attachments`) != 0 || s.storageOf("alice").used != 0 {
		t.Fatal("the file of a removed part must be deleted and its space returned")
	}
}

// The janitor frees unfinished uploads and attachments nobody used (docs/design/01 section 6.2).
func TestJanitorRemovesStaleUploads(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	uid := s.lookupUser("alice")
	unused := uuid.NewString()
	u.upload(unused, "unused.bin", "application/octet-stream", randomBytes(50_000))
	kept := uuid.NewString()
	u.upload(kept, "kept.bin", "application/octet-stream", randomBytes(50_000))
	u.attachNote(kept)
	// An upload that started and never finished (reservation held).
	if _, err := s.db.Admin.Exec(t.Context(), `update user_storage set reserved_bytes = 1000 where user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Admin.Exec(t.Context(), `insert into blobs (id, user_id, size_bytes, reserved_bytes) values (gen_random_uuid(), $1, 1000, 1000)`, uid); err != nil {
		t.Fatal(err)
	}
	// Nothing is stale yet.
	if err := s.svc.Blobs.ReleaseStale(t.Context(), uid, time.Hour); err != nil {
		t.Fatal(err)
	}
	if s.count(`select count(*) from attachments`) != 2 || s.count(`select count(*) from blobs where not complete`) != 1 {
		t.Fatal("the janitor removed something that is not old enough")
	}
	// The search trigger insists on a user context, even for a superuser (0005), so age the rows as the user.
	tx, err := s.db.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `select set_config('app.user_id', $1, true)`, uid.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `update blobs set created_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `update attachments set created_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.svc.Blobs.ReleaseStale(t.Context(), uid, time.Hour); err != nil {
		t.Fatal(err)
	}
	if s.count(`select count(*) from attachments`) != 1 || s.count(`select count(*) from blobs`) != 1 {
		t.Fatalf("after the janitor: %d attachments, %d blobs (the linked one must stay)", s.count(`select count(*) from attachments`), s.count(`select count(*) from blobs`))
	}
	if st := s.storageOf("alice"); st.reserved != 0 || st.used != 50_000 {
		t.Fatalf("storage: %+v", st)
	}
}

// Memory stays flat however large the file: uploads and downloads hold one chunk at a time
// (CORE-A8, NFR-API4, the guardrail of tech-stack section 3.2). The whole process, including
// the test's own client, stays under a small soft memory limit while moving 200 MiB.
func TestStreamingMemoryStaysFlat(t *testing.T) {
	if testing.Short() {
		t.Skip("moves 200 MiB")
	}
	s := newStackWith(t, func(c *config.Config) { c.MaxAttachmentBytes = 512 << 20 })
	u := s.appUser("alice")
	old := debug.SetMemoryLimit(96 << 20)
	defer debug.SetMemoryLimit(old)

	var peak atomic.Uint64
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak.Load() {
					peak.Store(m.HeapInuse)
				}
			}
		}
	}()
	defer close(stop)

	const size = 200 << 20
	id := uuid.NewString()
	pr, pw := io.Pipe()
	sum := sha256.New()
	go func() { // generate the body on the fly: the test itself holds no large buffer
		block := randomBytes(1 << 20)
		for written := 0; written < size; written += len(block) {
			sum.Write(block)
			if _, err := pw.Write(block); err != nil {
				return
			}
		}
		_ = pw.Close()
	}()
	res := u.c.doRaw("PUT", "/api/v1/attachments/"+id, pr, size, map[string]string{"X-Filename": "big.bin", "Content-Type": "application/octet-stream"})
	if res.Status != 201 {
		t.Fatalf("upload: %d %s", res.Status, res.Body)
	}
	u.attachNote(id)

	req, _ := http.NewRequest("GET", s.user.URL+"/api/v1/attachments/"+id, nil)
	for k, v := range u.c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	dl, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got := sha256.New()
	n, err := io.Copy(got, dl.Body) // hashed as it streams, never buffered
	_ = dl.Body.Close()
	if err != nil || n != size || !bytes.Equal(got.Sum(nil), sum.Sum(nil)) {
		t.Fatalf("download: %d bytes, err=%v, identical=%v", n, err, bytes.Equal(got.Sum(nil), sum.Sum(nil)))
	}
	mib := peak.Load() >> 20
	t.Logf("peak heap in use while moving %d MiB up and down: %d MiB", size>>20, mib)
	if mib > 64 {
		t.Fatalf("memory grew with the file: peak heap %d MiB", mib)
	}
}
