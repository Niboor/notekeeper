package bot

import (
	"context"
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
// as ignored, and the user is told once why (MX-2, SEC-MX-1).
func (b *Bot) roomAllowed(ctx context.Context, room id.RoomID) bool {
	b.mu.Lock()
	if ok, cached := b.dmCache[room]; cached {
		b.mu.Unlock()
		return ok
	}
	b.mu.Unlock()

	if ignored, err := b.rooms.isIgnored(ctx, room); err != nil {
		b.log.Warn("could not read room state", "room", room, "error", err)
		return false
	} else if ignored {
		b.remember(room, false)
		return false
	}
	members, err := b.client.JoinedMembers(ctx, room)
	if err != nil {
		b.log.Warn("could not list room members", "room", room, "error", err)
		return false // unknown: do nothing rather than process a room we cannot vouch for
	}
	if len(members.Joined) == 2 {
		b.remember(room, true)
		return true
	}
	b.remember(room, false)
	notified, err := b.rooms.ignore(ctx, room)
	if err != nil {
		b.log.Warn("could not record ignored room", "room", room, "error", err)
		return false
	}
	if !notified {
		b.sendNotice(ctx, room, ignoredNotice)
		_ = b.rooms.markNotified(ctx, room)
	}
	b.log.Info("ignoring a room that is not a two-person direct message", "room", room, "members", len(members.Joined))
	return false
}

func (b *Bot) remember(room id.RoomID, dm bool) {
	b.mu.Lock()
	b.dmCache[room] = dm
	b.mu.Unlock()
}
