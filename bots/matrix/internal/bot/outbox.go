package bot

import (
	"context"
	"errors"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/Niboor/notekeeper/bots/sdk/botclient"
)

const (
	outboxWait      = 20 * time.Second // shorter than the HTTP client timeout
	outboxBatch     = 10
	unlinkedGoodbye = "This chat is no longer linked to a Notekeeper account, so I am leaving it. Invite me again to start over."
)

// Outbox delivers what Core wants said in chats: reminders, notices and the instruction to forget a
// chat (BOT-12, BOT-13, BOT-16). It claims items with a long poll and runs until ctx ends. An item
// that is not acknowledged comes back after its lease, so a crash here loses nothing; the item id
// is the Matrix transaction id, so a repeated send is deduplicated by the homeserver.
func (b *Bot) Outbox(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		items, err := b.core.ClaimOutbox(ctx, outboxWait, outboxBatch)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			b.met.claimFailures.Inc()
			b.log.Warn("could not claim outbox items", "error", err)
			sleep(ctx, min(time.Second<<min(failures, 5), 30*time.Second))
			continue
		}
		failures = 0
		for _, it := range items {
			if ctx.Err() != nil {
				return
			}
			b.deliver(ctx, it)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (b *Bot) deliver(ctx context.Context, it botclient.OutboxItem) {
	kind := string(it.Kind)
	res := b.send(ctx, it)
	b.met.outbox.WithLabelValues(kind, string(res.State)).Inc()
	if err := b.core.ReportOutbox(ctx, it.Id, res); err != nil && ctx.Err() == nil {
		b.log.Error("could not report an outbox result", "item", it.Id, "error", err)
	}
}

func (b *Bot) send(ctx context.Context, it botclient.OutboxItem) botclient.OutboxResult {
	room := id.RoomID(it.ConversationId)
	if it.Kind == botclient.Lifecycle {
		return b.forgetRoom(ctx, room, it)
	}
	// The room must still be a two-person chat with the person the item is for, checked now and not from
	// what was remembered: a reminder must never reach a group someone was added to, or a room the
	// person left (SEC-MX-1, SEC-MX-2). If the homeserver cannot say, try again later (CR-002).
	if res, ok := b.checkRecipient(ctx, room, it.ExternalUserId); !ok {
		return res
	}
	text, _ := it.Payload["text"].(string)
	if text == "" {
		return failure(botclient.FailedPermanent, "empty_message")
	}
	content := &event.MessageEventContent{MsgType: event.MsgNotice, Body: text}
	if it.Kind == botclient.Reminder {
		content.MsgType = event.MsgText // a reminder should ring; notices are quiet in many clients
	}
	resp, err := b.client.SendMessageEvent(ctx, room, event.EventMessage, content, mautrix.ReqSendEvent{TransactionID: it.Id.String()})
	if err != nil {
		return sendFailure(err)
	}
	ids := []string{resp.EventID.String()}
	if it.Kind == botclient.Reminder {
		b.sendFiles(ctx, room, it) // best effort: the reminder text is already delivered
	}
	return botclient.OutboxResult{State: botclient.Delivered, MessageIds: &ids}
}

// checkRecipient asks the homeserver who is in the room. ok is false when nothing may be sent, and
// res then says why.
func (b *Bot) checkRecipient(ctx context.Context, room id.RoomID, recipient string) (res botclient.OutboxResult, ok bool) {
	members, err := b.client.JoinedMembers(ctx, room)
	if err != nil {
		return sendFailure(err), false
	}
	if _, there := members.Joined[id.UserID(recipient)]; !there || len(members.Joined) != 2 {
		b.remember(room, false)
		return failure(botclient.FailedPermanent, "room_not_allowed"), false
	}
	b.remember(room, true)
	return res, true
}

// forgetRoom leaves and forgets a chat that was unlinked or whose user was deleted, and drops what
// the bot remembers about it (MX-12, BOT-16). A room that is already gone counts as done.
func (b *Bot) forgetRoom(ctx context.Context, room id.RoomID, it botclient.OutboxItem) botclient.OutboxResult {
	if reason, _ := it.Payload["reason"].(string); reason == "unlinked" {
		b.sendNotice(ctx, room, unlinkedGoodbye)
	}
	if _, err := b.client.LeaveRoom(ctx, room); err != nil && !roomGone(err) {
		return sendFailure(err)
	}
	if _, err := b.client.ForgetRoom(ctx, room); err != nil && !roomGone(err) {
		return sendFailure(err)
	}
	if err := b.rooms.forget(ctx, room); err != nil {
		return failure(botclient.FailedTransient, "could not forget room state")
	}
	b.mu.Lock()
	delete(b.dmCache, room)
	b.mu.Unlock()
	return botclient.OutboxResult{State: botclient.Delivered}
}

func roomGone(err error) bool {
	return errors.Is(err, mautrix.MForbidden) || errors.Is(err, mautrix.MNotFound)
}

func failure(state botclient.OutboxResultState, reason string) botclient.OutboxResult {
	return botclient.OutboxResult{State: state, Reason: &reason}
}

// sendFailure decides whether a failed send is worth retrying: a room we cannot write to will not
// get better, a homeserver that is down will.
func sendFailure(err error) botclient.OutboxResult {
	if roomGone(err) {
		return failure(botclient.FailedPermanent, "room_unavailable")
	}
	return failure(botclient.FailedTransient, "homeserver_error")
}
