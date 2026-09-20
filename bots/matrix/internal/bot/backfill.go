package bot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/Niboor/notekeeper/bots/sdk"
)

// Backfill limits. A gap is filled page by page, newest first, until it reaches what Core already
// holds. The page limit bounds the work of one sync response; Core deduplicates by event id, so
// filling more than necessary is harmless.
const (
	backfillPageSize  = 100
	backfillMaxPages  = 20
	backfillOverlap   = 10 * time.Minute // fetch this far before Core's newest message, to be safe against clock skew
	backfillAttempts  = 5
	backfillNoCursor  = "no message held"
	backfillTruncated = "truncated"
)

// historyClient is the part of the Matrix client that backfilling needs.
type historyClient interface {
	Messages(ctx context.Context, roomID id.RoomID, from, to string, dir mautrix.Direction, filter *mautrix.FilterPart, limit int) (*mautrix.RespMessages, error)
}

// cursorSource tells how far Core has got in a conversation: the platform time of its newest
// message, or nil when it holds none.
type cursorSource func(ctx context.Context, conversation string) (*time.Time, error)

// noteEvents are the event types Core can use: everything else in the gap (membership, receipts,
// typing, name changes) is not asked for.
var noteEvents = []event.Type{event.EventMessage, event.EventEncrypted, event.EventRedaction}

// fillGap returns the events that fell out of a limited sync timeline, oldest first. It walks
// backwards from prevBatch (the token just before the returned window) until it is past Core's newest
// message for the room, the room's start, or the page limit. truncated reports that the limit was hit
// while events newer than Core's cursor might remain.
func fillGap(ctx context.Context, hist historyClient, cursor cursorSource, room id.RoomID, prevBatch string) (events []*event.Event, truncated bool, err error) {
	if prevBatch == "" {
		return nil, false, nil
	}
	held, err := cursor(ctx, room.String())
	if err != nil {
		return nil, false, err
	}
	var stopAt int64 // events at or before this are already in Core; 0 = read on to the page limit
	if held != nil {
		stopAt = held.Add(-backfillOverlap).UnixMilli()
	}
	filter := &mautrix.FilterPart{Types: noteEvents}
	from := prevBatch
	for page := 0; page < backfillMaxPages; page++ {
		resp, err := hist.Messages(ctx, room, from, "", mautrix.DirectionBackward, filter, backfillPageSize)
		if err != nil {
			return nil, false, err
		}
		reachedKnown := false
		for _, evt := range resp.Chunk { // newest first
			if stopAt > 0 && evt.Timestamp < stopAt {
				reachedKnown = true
				break
			}
			events = append(events, evt)
		}
		if reachedKnown || resp.End == "" || resp.End == from || len(resp.Chunk) == 0 {
			slices.Reverse(events)
			return events, false, nil
		}
		from = resp.End
	}
	slices.Reverse(events)
	return events, true, nil
}

// backfillGaps is a sync listener. When the homeserver marks a room's timeline as limited it has
// left out older events; the bot fetches them and puts them in front of the response's own events, so
// they are handled in order and covered by the same commit of the sync token (CR-001, BOT-B2, MX-9).
// It never stops the sync from being processed: a room that cannot be filled is logged and counted.
func (b *Bot) backfillGaps(ctx context.Context, res *mautrix.RespSync, since string) bool {
	if since == "" {
		return true // the first sync of a new deployment: old history is not ours to import
	}
	for room, data := range res.Rooms.Join {
		if data == nil || !data.Timeline.Limited {
			continue
		}
		if !b.allowedOrStall(ctx, room) {
			continue
		}
		gap, truncated, err := b.fillWithRetry(ctx, room, data.Timeline.PrevBatch)
		if err != nil {
			// Do not go on: the token would move past events that were never seen.
			panic(fmt.Errorf("could not fill a gap in room %s: %w", room, err))
		}
		if truncated {
			b.met.gaps.WithLabelValues(backfillTruncated).Inc()
			b.log.Error("a gap in a room was larger than the backfill limit; older messages may be missing", "room", room, "fetched", len(gap))
		}
		if len(gap) > 0 {
			b.met.gaps.WithLabelValues("filled").Inc()
			b.log.Info("filled a gap in a room's history", "room", room, "events", len(gap))
			data.Timeline.Events = append(gap, data.Timeline.Events...)
		}
	}
	return true
}

func (b *Bot) fillWithRetry(ctx context.Context, room id.RoomID, prevBatch string) (gap []*event.Event, truncated bool, err error) {
	for attempt := 1; ; attempt++ {
		gap, truncated, err = fillGap(ctx, b.historySource(), b.cursorSource(), room, prevBatch)
		if err == nil || ctx.Err() != nil {
			return gap, truncated, err
		}
		var http *mautrix.HTTPError
		if errors.As(err, &http) && http.Response != nil && http.Response.StatusCode >= 400 && http.Response.StatusCode < 500 && http.Response.StatusCode != 429 {
			// The homeserver will not show this history (for example it was not visible to the bot). No
			// retry helps: say so and carry on with what the sync gave us.
			b.log.Error("the homeserver refuses to show a gap in a room's history", "room", room, "error", err)
			return nil, false, nil
		}
		if attempt >= backfillAttempts {
			return nil, false, err
		}
		b.log.Warn("could not fill a gap; retrying", "room", room, "attempt", attempt, "error", err)
		sleep(ctx, b.lookupBackoff(attempt))
	}
}

func (b *Bot) historySource() historyClient {
	if b.history != nil {
		return b.history
	}
	return b.client
}

func (b *Bot) cursorSource() cursorSource {
	if b.cursor != nil {
		return b.cursor
	}
	return func(ctx context.Context, conversation string) (*time.Time, error) {
		at, err := b.core.ConversationCursor(ctx, conversation)
		var perm *sdk.PermanentError
		if errors.As(err, &perm) {
			return nil, nil // Core will not say: read the history up to the page limit instead
		}
		return at, err
	}
}
