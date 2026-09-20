package bot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type fakeRooms struct {
	ignored, recovered map[id.RoomID]bool
	notified           bool
}

func newFakeRooms() *fakeRooms {
	return &fakeRooms{ignored: map[id.RoomID]bool{}, recovered: map[id.RoomID]bool{}}
}
func (f *fakeRooms) ignore(_ context.Context, r id.RoomID) (bool, error) {
	f.ignored[r] = true
	return f.notified, nil
}
func (f *fakeRooms) markNotified(context.Context, id.RoomID) error { f.notified = true; return nil }
func (f *fakeRooms) recover(_ context.Context, r id.RoomID) error {
	if f.ignored[r] {
		f.recovered[r] = true
		delete(f.ignored, r)
	}
	return nil
}
func (f *fakeRooms) forget(context.Context, id.RoomID) error { return nil }

// A homeserver whose "who is in the room" answers are scripted per call.
func memberRig(t *testing.T, answers ...string) (*Bot, *atomic.Int32) {
	var calls atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		a := answers[min(n, len(answers))-1]
		w.Header().Set("Content-Type", "application/json")
		switch a {
		case "502":
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN","error":"bad gateway"}`))
		case "403":
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"not in room"}`))
		case "dm":
			_, _ = w.Write([]byte(`{"joined":{"@a:x":{},"@bot:x":{}}}`))
		case "group":
			_, _ = w.Write([]byte(`{"joined":{"@a:x":{},"@b:x":{},"@bot:x":{}}}`))
		default: // a sent message
			_, _ = w.Write([]byte(`{"event_id":"$n"}`))
		}
	}))
	t.Cleanup(hs.Close)
	client, err := mautrix.NewClient(hs.URL, "@bot:x", "")
	if err != nil {
		t.Fatal(err)
	}
	client.DefaultHTTPRetries = 0
	b := &Bot{log: slog.New(slog.NewTextHandler(io.Discard, nil)), met: newMetrics(), client: client, rooms: newFakeRooms(),
		dmCache: map[id.RoomID]bool{}, undecryptable: map[id.RoomID]time.Time{},
		LookupBackoff: func(int) time.Duration { return time.Millisecond }}
	return b, &calls
}

// A homeserver that cannot answer must not be mistaken for "not a direct message": the message would be
// dropped with the sync token moving on (CR-002, NFR-R1).
func TestUnknownRoomStateIsRetriedNotTreatedAsNo(t *testing.T) {
	b, calls := memberRig(t, "502", "502", "dm")
	if !b.allowedOrStall(t.Context(), "!r:x") {
		t.Fatal("the room is a direct message once the homeserver answers")
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if ok, cached := b.dmCache["!r:x"]; !ok || !cached {
		t.Fatal("the answer was not remembered")
	}
}

func TestUnknownRoomStateThatNeverClearsStopsTheBatch(t *testing.T) {
	b, calls := memberRig(t, "502")
	defer func() {
		if recover() == nil {
			t.Fatal("expected the handler to fail so the batch is replayed")
		}
		if calls.Load() != roomLookupAttempts {
			t.Fatalf("calls = %d", calls.Load())
		}
		if _, cached := b.dmCache["!r:x"]; cached {
			t.Fatal("an unknown answer must not be remembered")
		}
	}()
	b.allowedOrStall(t.Context(), "!r:x")
}

func TestRoomWeLeftIsAPlainNo(t *testing.T) {
	b, _ := memberRig(t, "403")
	if b.allowedOrStall(t.Context(), "!gone:x") {
		t.Fatal("not a member: not ours")
	}
	if _, cached := b.dmCache["!gone:x"]; cached {
		t.Fatal("a room we may return to must not be remembered as refused")
	}
}

// A group that shrinks back to two people is a direct message again (CR-005).
func TestIgnoredRoomRecovers(t *testing.T) {
	b, _ := memberRig(t, "group", "dm")
	rooms := b.rooms.(*fakeRooms)
	if b.roomAllowed(t.Context(), "!r:x") || !rooms.ignored["!r:x"] {
		t.Fatal("a three-person room must be ignored")
	}
	delete(b.dmCache, "!r:x") // what onMember does when someone leaves
	if !b.roomAllowed(t.Context(), "!r:x") || !rooms.recovered["!r:x"] {
		t.Fatal("the room must work again")
	}
}

func TestUndecryptableMessagesAreAnsweredOncePerWhile(t *testing.T) {
	b, calls := memberRig(t, "dm", "sent", "dm", "sent")
	evt := &event.Event{RoomID: "!r:x", Sender: "@a:x"}
	b.tellUndecryptable(evt)
	b.tellUndecryptable(evt)
	b.tellUndecryptable(evt)
	if calls.Load() != 2 { // one membership lookup and one notice, not one per message
		t.Fatalf("calls = %d", calls.Load())
	}
	b.tellUndecryptable(&event.Event{RoomID: "!r:x", Sender: "@bot:x"})
	if calls.Load() != 2 {
		t.Fatal("the bot's own events are never answered")
	}
}

type fakeHistory struct {
	pages [][]*event.Event // newest page first
	seen  []string
	err   error
}

func (f *fakeHistory) Messages(_ context.Context, _ id.RoomID, from, _ string, dir mautrix.Direction, _ *mautrix.FilterPart, _ int) (*mautrix.RespMessages, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.seen = append(f.seen, from)
	if dir != mautrix.DirectionBackward {
		return nil, errors.New("must page backwards")
	}
	i := len(f.seen) - 1
	if i >= len(f.pages) {
		return &mautrix.RespMessages{}, nil
	}
	end := "p" + string(rune('1'+i))
	return &mautrix.RespMessages{Chunk: f.pages[i], End: end}, nil
}

func ev(id string, ms int64) *event.Event {
	return &event.Event{ID: idOf(id), Timestamp: ms}
}
func idOf(s string) id.EventID { return id.EventID(s) }

func idsOf(evs []*event.Event) string {
	var out []string
	for _, e := range evs {
		out = append(out, e.ID.String())
	}
	return strings.Join(out, ",")
}

func cursorAt(ms int64) cursorSource {
	return func(context.Context, string) (*time.Time, error) {
		if ms == 0 {
			return nil, nil
		}
		t := time.UnixMilli(ms)
		return &t, nil
	}
}

// The events the homeserver left out are fetched newest to oldest, stop at what Core already holds, and
// come back oldest first so they are handled in order (CR-001, BOT-B2).
func TestGapIsFilledBackToWhatCoreHolds(t *testing.T) {
	base := int64(10_000_000_000)
	overlap := backfillOverlap.Milliseconds()
	h := &fakeHistory{pages: [][]*event.Event{
		{ev("$9", base+9000), ev("$8", base+8000)},
		{ev("$7", base+7000), ev("$old", base-overlap-1)}, // older than Core's newest message minus the overlap
		{ev("$never", base-99999)},
	}}
	got, truncated, err := fillGap(t.Context(), h, cursorAt(base), "!r:x", "prev")
	if err != nil || truncated {
		t.Fatalf("err %v truncated %v", err, truncated)
	}
	if idsOf(got) != "$7,$8,$9" {
		t.Fatalf("events = %s", idsOf(got))
	}
	if len(h.seen) != 2 || h.seen[0] != "prev" || h.seen[1] != "p1" {
		t.Fatalf("pages fetched: %v", h.seen)
	}
}

func TestGapWithNothingHeldReadsToTheStart(t *testing.T) {
	h := &fakeHistory{pages: [][]*event.Event{{ev("$2", 2)}, {ev("$1", 1)}}}
	got, truncated, err := fillGap(t.Context(), h, cursorAt(0), "!r:x", "prev")
	if err != nil || truncated || idsOf(got) != "$1,$2" {
		t.Fatalf("got %s truncated %v err %v", idsOf(got), truncated, err)
	}
}

func TestGapLargerThanTheLimitIsReportedNotHidden(t *testing.T) {
	var pages [][]*event.Event
	for i := 0; i < backfillMaxPages+5; i++ {
		pages = append(pages, []*event.Event{ev("$e", 1_000_000+int64(i))})
	}
	h := &fakeHistory{pages: pages}
	got, truncated, err := fillGap(t.Context(), h, cursorAt(1), "!r:x", "prev")
	if err != nil || !truncated || len(got) != backfillMaxPages {
		t.Fatalf("got %d truncated %v err %v", len(got), truncated, err)
	}
}

func TestNoGapWithoutPrevBatchAndErrorsPropagate(t *testing.T) {
	h := &fakeHistory{err: errors.New("down")}
	if got, _, err := fillGap(t.Context(), h, cursorAt(1), "!r:x", ""); err != nil || got != nil {
		t.Fatalf("no token: %v %v", got, err)
	}
	if _, _, err := fillGap(t.Context(), h, cursorAt(1), "!r:x", "prev"); err == nil {
		t.Fatal("a failing homeserver must not look like an empty gap")
	}
}

// Only a real gap is filled, and only for rooms the bot may serve; the events end up in front of the
// response's own, in order.
func TestBackfillPrependsTheGapToALimitedTimeline(t *testing.T) {
	b, _ := memberRig(t, "dm")
	b.dmCache["!r:x"] = true
	hist := &fakeHistory{pages: [][]*event.Event{{ev("$2", 2000), ev("$1", 1000)}}}
	b.history = hist
	b.cursor = cursorAt(0)
	res := &mautrix.RespSync{}
	res.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{
		"!r:x": {Timeline: mautrix.SyncTimeline{Limited: true, PrevBatch: "prev", SyncEventsList: mautrix.SyncEventsList{Events: []*event.Event{ev("$3", 3000)}}}},
		"!q:x": {Timeline: mautrix.SyncTimeline{SyncEventsList: mautrix.SyncEventsList{Events: []*event.Event{ev("$q", 1)}}}},
	}
	b.backfillGaps(t.Context(), res, "s1")
	if got := idsOf(res.Rooms.Join["!r:x"].Timeline.Events); got != "$1,$2,$3" {
		t.Fatalf("limited room: %s", got)
	}
	if got := idsOf(res.Rooms.Join["!q:x"].Timeline.Events); got != "$q" {
		t.Fatalf("complete room touched: %s", got)
	}
	// The first sync of a deployment must not import history.
	first := &mautrix.RespSync{}
	first.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{"!r:x": {Timeline: mautrix.SyncTimeline{Limited: true, PrevBatch: "prev"}}}
	hist.seen = nil
	b.backfillGaps(t.Context(), first, "")
	if len(hist.seen) != 0 {
		t.Fatal("the initial sync was backfilled")
	}
}
