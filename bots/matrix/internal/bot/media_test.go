package bot

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/Niboor/notekeeper/bots/matrix/internal/config"
	"github.com/Niboor/notekeeper/bots/matrix/internal/normalise"
	"github.com/Niboor/notekeeper/bots/sdk"
)

// mediaRig is a homeserver that serves one file and a Core that accepts uploads, to test moving a
// file from chat to Core without a real Matrix stack.
type mediaRig struct {
	bot *Bot

	mu         sync.Mutex
	served     []byte
	status     int // homeserver answer, 0 = 200
	failFirst  int // homeserver answers 500 this many times first
	downloads  atomic.Int32
	uploaded   []byte
	uploads    atomic.Int32
	coreStatus int // Core's answer to the upload, 0 = 201
	coreCode   string
	lastHeader http.Header
	chunked    bool // homeserver does not announce a length
}

func newMediaRig(t *testing.T) *mediaRig {
	r := &mediaRig{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.downloads.Add(1)
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.failFirst > 0 {
			r.failFirst--
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.status != 0 {
			w.WriteHeader(r.status)
			return
		}
		if r.chunked {
			w.(http.Flusher).Flush() // forces chunked encoding: no Content-Length
		} else {
			w.Header().Set("Content-Length", itoa(len(r.served)))
		}
		_, _ = w.Write(r.served)
	}))
	t.Cleanup(hs.Close)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.uploads.Add(1)
		r.lastHeader = req.Header.Clone()
		if req.ContentLength != int64(len(body)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.coreStatus != 0 {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(r.coreStatus)
			_, _ = w.Write([]byte(`{"code":"` + r.coreCode + `","title":"x","status":1}`))
			return
		}
		r.uploaded = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"00000000-0000-0000-0000-000000000000","filename":"x","media_type":"x","size":1}`))
	}))
	t.Cleanup(core.Close)
	client, err := mautrix.NewClient(hs.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := sdk.NewClient(core.URL, "nkb.x.y", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	sc.Backoff = func(int) time.Duration { return time.Millisecond }
	r.bot = &Bot{cfg: config.Config{MaxAttachmentBytes: 1 << 20}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), core: sc, met: newMetrics(), client: client}
	return r
}

func itoa(n int) string { return strconv.Itoa(n) }

func idURI(s string) id.ContentURIString { return id.ContentURIString(s) }

func imageEvent(url string, size int, file *event.EncryptedFileInfo) (*event.Event, normalise.Result) {
	c := &event.MessageEventContent{MsgType: event.MsgImage, Body: "caption", FileName: "cat.png", Info: &event.FileInfo{MimeType: "image/png", Size: size}}
	if file != nil {
		c.File = file
	} else {
		c.URL = idURI(url)
	}
	evt := &event.Event{ID: "$img", RoomID: "!r:x", Sender: "@alice:x", Timestamp: 1_800_000_000_000, Type: event.EventMessage, Content: event.Content{Parsed: c}}
	return evt, normalise.Message(evt)
}

// A file goes from the homeserver to Core as it is, streamed with its exact length, and the part
// then points at the upload (BOT-6, MX-5).
func TestPlainFileIsMovedToCore(t *testing.T) {
	r := newMediaRig(t)
	r.served = randomData(300_000)
	evt, res := imageEvent("mxc://x/abc", len(r.served), nil)
	r.bot.fetchMedia(t.Context(), evt, &res)
	parts := *res.Event.Parts
	if parts[0].UploadId == nil || string(parts[0].Type) != "attachment" || !bytes.Equal(r.uploaded, r.served) {
		t.Fatalf("part %+v, uploaded %d bytes", parts[0], len(r.uploaded))
	}
	if r.lastHeader.Get("X-External-User") != "@alice:x" || r.lastHeader.Get("X-Filename") != "cat.png" || r.lastHeader.Get("X-Media-Type") != "image/png" {
		t.Fatalf("headers: %v", r.lastHeader)
	}
	// The same message always uploads to the same id, so a retry never piles up orphans.
	first := *parts[0].UploadId
	_, res2 := imageEvent("mxc://x/abc", len(r.served), nil)
	evt2, _ := imageEvent("mxc://x/abc", len(r.served), nil)
	r.bot.fetchMedia(t.Context(), evt2, &res2)
	if *(*res2.Event.Parts)[0].UploadId != first {
		t.Fatal("the upload id must be stable for a message")
	}
}

// Encrypted files are decrypted while streaming; a file whose checksum does not match is never
// saved as if it were fine (MX-3, MX-5).
func TestEncryptedFileIsDecryptedAndVerified(t *testing.T) {
	plain := randomData(200_000)
	enc := attachment.NewEncryptedFile()
	cipher := append([]byte(nil), plain...)
	enc.EncryptInPlace(cipher)
	// Through JSON, as in a real event: the decoded key cache must start empty.
	raw, _ := json.Marshal(enc)
	file := &event.EncryptedFileInfo{URL: "mxc://x/enc"}
	if err := json.Unmarshal(raw, &file.EncryptedFile); err != nil {
		t.Fatal(err)
	}

	r := newMediaRig(t)
	r.served = cipher
	evt, res := imageEvent("", len(plain), file)
	r.bot.fetchMedia(t.Context(), evt, &res)
	if (*res.Event.Parts)[0].UploadId == nil || !bytes.Equal(r.uploaded, plain) {
		t.Fatalf("decrypted upload wrong: %+v reason %v", (*res.Event.Parts)[0], *(*res.Event.Parts)[0].Reason)
	}

	// One flipped bit in transit: the hash check fails after the bytes went out, and the part is marked failed.
	bad := append([]byte(nil), cipher...)
	bad[1000] ^= 1
	r2 := newMediaRig(t)
	r2.served = bad
	evt, res = imageEvent("", len(plain), file)
	r2.bot.fetchMedia(t.Context(), evt, &res)
	p := (*res.Event.Parts)[0]
	if string(p.Type) != "attachment_failed" || *p.Reason != "corrupt" || p.UploadId != nil {
		t.Fatalf("tampered file: %+v", p)
	}
}

// Every way a file can fail leaves the message intact with a reason, and refusals are cheap (CORE-A9, MX-5).
func TestFileFailuresBecomeFailedAttachments(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(r *mediaRig)
		size   int
		reason string
		fetch  int32 // homeserver requests expected
	}{
		{"claimed size too large", func(r *mediaRig) {}, 5 << 20, "too_large", 0},
		{"real size too large", func(r *mediaRig) { r.served = make([]byte, 2<<20) }, 1000, "too_large", 1},
		{"gone from the homeserver", func(r *mediaRig) { r.status = 404 }, 1000, "download_failed", 1},
		{"no length announced", func(r *mediaRig) { r.served = []byte("x"); r.chunked = true }, 1, "size_unknown", 1},
		{"empty", func(r *mediaRig) { r.served = nil }, 0, "download_failed", 1},
		{"user over quota", func(r *mediaRig) { r.served = []byte("data"); r.coreStatus, r.coreCode = 413, "quota_exceeded" }, 4, "quota_exceeded", 1},
		{"core refuses the type", func(r *mediaRig) { r.served = []byte("data"); r.coreStatus, r.coreCode = 400, "invalid_attachment" }, 4, "upload_failed", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newMediaRig(t)
			tc.setup(r)
			evt, res := imageEvent("mxc://x/abc", tc.size, nil)
			r.bot.fetchMedia(t.Context(), evt, &res)
			parts := *res.Event.Parts
			if string(parts[0].Type) != "attachment_failed" || parts[0].Reason == nil || *parts[0].Reason != tc.reason {
				t.Fatalf("part %+v, want reason %s", parts[0], tc.reason)
			}
			if len(parts) != 2 || parts[1].Text == nil || *parts[1].Text != "caption" {
				t.Fatalf("the caption must survive: %+v", parts)
			}
			if got := r.downloads.Load(); got != tc.fetch {
				t.Fatalf("homeserver requests: %d, want %d", got, tc.fetch)
			}
		})
	}
}

// A homeserver hiccup is retried; a Core hiccup too; but not forever (CORE-A9).
func TestTransientTroubleIsRetriedAFewTimes(t *testing.T) {
	r := newMediaRig(t)
	r.served = []byte("some bytes")
	r.failFirst = 2
	evt, res := imageEvent("mxc://x/abc", len(r.served), nil)
	r.bot.fetchMedia(t.Context(), evt, &res)
	if (*res.Event.Parts)[0].UploadId == nil || r.downloads.Load() != 3 {
		t.Fatalf("after two failures: %+v after %d downloads", (*res.Event.Parts)[0], r.downloads.Load())
	}

	down := newMediaRig(t)
	down.served = []byte("some bytes")
	down.failFirst = 100
	evt, res = imageEvent("mxc://x/abc", len(down.served), nil)
	down.bot.fetchMedia(t.Context(), evt, &res)
	p := (*res.Event.Parts)[0]
	if string(p.Type) != "attachment_failed" || *p.Reason != "upload_failed" || down.downloads.Load() != int32(sdk.UploadAttempts) {
		t.Fatalf("a homeserver that stays down: %+v after %d downloads", p, down.downloads.Load())
	}
}

func randomData(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
