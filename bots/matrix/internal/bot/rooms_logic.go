package bot

import (
	"context"
	"fmt"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// onMember handles invites to the bot and membership changes in rooms it is in (MX-1, MX-2).
func (b *Bot) onMember(ctx context.Context, evt *event.Event) {
	member := evt.Content.AsMember()
	if member == nil {
		return
	}
	if evt.GetStateKey() == b.client.UserID.String() {
		if member.Membership == event.MembershipInvite {
			b.onInvite(ctx, evt, member)
		}
		return
	}
	// Someone else joined or left a room: it may have stopped being a two-person chat. Invites do
	// not change who is in the room, so they are not looked at.
	if member.Membership != event.MembershipJoin && member.Membership != event.MembershipLeave {
		return
	}
	b.mu.Lock()
	_, known := b.dmCache[evt.RoomID]
	delete(b.dmCache, evt.RoomID)
	b.mu.Unlock()
	if known {
		b.roomAllowed(ctx, evt.RoomID) // re-checks and, if needed, marks the room ignored
	}
}

// onInvite accepts direct-message invites only, and only a few per minute (SEC-MX-1). Anything
// else is declined: the bot has no business in group rooms.
func (b *Bot) onInvite(ctx context.Context, evt *event.Event, member *event.MemberEventContent) {
	if !b.inviteAllowed() {
		b.log.Warn("invite rate limit reached; declining", "room", evt.RoomID)
		_, _ = b.client.LeaveRoom(ctx, evt.RoomID)
		return
	}
	if !member.IsDirect {
		b.log.Info("declining an invite to a room that is not a direct message", "room", evt.RoomID)
		if _, err := b.client.LeaveRoom(ctx, evt.RoomID); err != nil {
			b.log.Warn("could not decline invite", "room", evt.RoomID, "error", err)
		}
		return
	}
	if _, err := b.client.JoinRoomByID(ctx, evt.RoomID); err != nil {
		b.log.Warn("could not join", "room", evt.RoomID, "error", err)
		return
	}
	b.roomAllowed(ctx, evt.RoomID) // a "direct" room with extra members is still refused
}

func (b *Bot) inviteAllowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := time.Now().Add(-time.Minute)
	kept := b.invites[:0]
	for _, t := range b.invites {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	b.invites = kept
	if len(b.invites) >= maxInvitesPerMinute {
		return false
	}
	b.invites = append(b.invites, time.Now())
	return true
}

// roomAllowed reports whether the bot may process a room: it must be a direct message with
// exactly two joined members, the bot and one person. A room found not to be one is remembered
// as ignored, and the user is told once why (MX-2, SEC-MX-1). Membership is always decided from
// the homeserver (and remembered until a member joins or leaves), never from what was stored
// earlier, so a room that became a two-person chat again works again.
//
// It answers "no" for both "not allowed" and "could not find out"; callers that would lose a
// message on a wrong "no" use roomState or allowedOrStall instead.
func (b *Bot) roomAllowed(ctx context.Context, room id.RoomID) bool {
	ok, err := b.roomState(ctx, room)
	if err != nil {
		b.log.Warn("could not list room members", "room", room, "error", err)
		return false
	}
	return ok
}

// roomState is roomAllowed with the unknown case kept apart: err != nil means the homeserver could
// not say, which is not the same as "not a direct message" (CR-002). A room the bot is no longer in
// is a plain "no" and is not remembered.
func (b *Bot) roomState(ctx context.Context, room id.RoomID) (bool, error) {
	b.mu.Lock()
	if ok, cached := b.dmCache[room]; cached {
		b.mu.Unlock()
		return ok, nil
	}
	b.mu.Unlock()

	members, err := b.client.JoinedMembers(ctx, room)
	if err != nil {
		if roomGone(err) {
			return false, nil
		}
		return false, err
	}
	if len(members.Joined) == 2 {
		b.remember(room, true)
		if err := b.rooms.recover(ctx, room); err != nil {
			b.log.Warn("could not record that a room is a direct message again", "room", room, "error", err)
		}
		return true, nil
	}
	b.remember(room, false)
	notified, err := b.rooms.ignore(ctx, room)
	if err != nil {
		b.log.Warn("could not record ignored room", "room", room, "error", err)
		return false, nil
	}
	if !notified {
		b.sendNotice(ctx, room, ignoredNotice)
		_ = b.rooms.markNotified(ctx, room)
	}
	b.log.Info("ignoring a room that is not a two-person direct message", "room", room, "members", len(members.Joined))
	return false, nil
}

// roomLookupAttempts is how often allowedOrStall asks before it gives up.
const roomLookupAttempts = 5

// allowedOrStall is roomAllowed for events that must not be lost. A homeserver that cannot answer
// is retried with backoff; if it stays silent the handler fails, which stops the sync loop without
// committing the batch, and the restart replays it (syncack). Wrongly answering "no" would drop the
// message with the sync token advancing over it (NFR-R1).
func (b *Bot) allowedOrStall(ctx context.Context, room id.RoomID) bool {
	for attempt := 1; ; attempt++ {
		ok, err := b.roomState(ctx, room)
		if err == nil {
			return ok
		}
		if ctx.Err() != nil {
			return false
		}
		if attempt >= roomLookupAttempts {
			panic(fmt.Errorf("cannot tell whether room %s is a direct message: %w", room, err))
		}
		b.log.Warn("could not list room members; retrying", "room", room, "attempt", attempt, "error", err)
		sleep(ctx, b.lookupBackoff(attempt))
	}
}

func (b *Bot) lookupBackoff(attempt int) time.Duration {
	if b.LookupBackoff != nil {
		return b.LookupBackoff(attempt)
	}
	return time.Second << (attempt - 1)
}

func (b *Bot) remember(room id.RoomID, dm bool) {
	b.mu.Lock()
	b.dmCache[room] = dm
	b.mu.Unlock()
}
