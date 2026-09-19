package syncack

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const user = id.UserID("@bot:example.org")

func newFixture() (*Syncer, *mautrix.MemorySyncStore) {
	inner := mautrix.NewMemorySyncStore()
	st := NewStore(inner)
	return NewSyncer(st, func() id.UserID { return user }), inner
}

func stored(t *testing.T, s *mautrix.MemorySyncStore) string {
	t.Helper()
	tok, err := s.LoadNextBatch(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestTokenIsDeferredUntilProcessed(t *testing.T) {
	syncer, inner := newFixture()
	ctx := context.Background()

	// mautrix calls SaveNextBatch before ProcessResponse; nothing may be persisted yet.
	if err := syncer.store.SaveNextBatch(ctx, user, "s2"); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, inner); got != "" {
		t.Fatalf("token persisted before processing: %q", got)
	}
	if err := syncer.ProcessResponse(ctx, &mautrix.RespSync{NextBatch: "s2"}, "s1"); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, inner); got != "s2" {
		t.Fatalf("token after successful processing = %q, want s2", got)
	}
}

func TestTokenNotCommittedWhenAHandlerPanics(t *testing.T) {
	syncer, inner := newFixture()
	ctx := context.Background()
	syncer.OnEventType(event.EventMessage, func(context.Context, *event.Event) { panic("core unreachable") })

	if err := syncer.store.SaveNextBatch(ctx, user, "s2"); err != nil {
		t.Fatal(err)
	}
	res := &mautrix.RespSync{NextBatch: "s2"}
	res.Rooms.Join = map[id.RoomID]*mautrix.SyncJoinedRoom{
		"!r:example.org": {Timeline: mautrix.SyncTimeline{SyncEventsList: mautrix.SyncEventsList{Events: []*event.Event{{
			Type: event.EventMessage, Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "hi"}},
		}}}}},
	}
	if err := syncer.ProcessResponse(ctx, res, "s1"); err == nil {
		t.Fatal("expected ProcessResponse to report the panic")
	}
	if got := stored(t, inner); got != "" {
		t.Fatalf("token committed despite a failed batch: %q", got)
	}
}

func TestCommitWithoutPendingIsANoop(t *testing.T) {
	syncer, inner := newFixture()
	if err := syncer.store.Commit(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, inner); got != "" {
		t.Fatalf("unexpected token %q", got)
	}
}

type failingStore struct{ mautrix.SyncStore }

func (failingStore) SaveNextBatch(context.Context, id.UserID, string) error {
	return errors.New("db down")
}

func TestCommitErrorIsReturned(t *testing.T) {
	st := NewStore(failingStore{mautrix.NewMemorySyncStore()})
	syncer := NewSyncer(st, func() id.UserID { return user })
	ctx := context.Background()
	_ = st.SaveNextBatch(ctx, user, "s2")
	if err := syncer.ProcessResponse(ctx, &mautrix.RespSync{NextBatch: "s2"}, "s1"); err == nil {
		t.Fatal("a failed commit must stop the sync loop so the batch is replayed")
	}
}
