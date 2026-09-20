package ingest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/store"
)

// Command is a chat command forwarded by a bot. The bot recognises commands only when the
// message body starts with `!` and a known name (MX-8, SEC-BOT-12); Core interprets them.
type Command struct {
	Name         string
	Args         string
	Sender       string
	Conversation string
	ReplyTo      string
	MessageID    string // the message that carried the command, for idempotency
	Timestamp    time.Time
}

// CommandOutcome is what the bot should show. Wording lives here, not in the bots (BOT-8).
type CommandOutcome struct {
	OK       bool
	Feedback Feedback
}

const helpText = "Everything you send me becomes a note in your Notekeeper Inbox.\n" +
	"Commands:\n" +
	"• `!link CODE` – link this chat to your account (create the code in the app under Settings → Chats)\n" +
	"• `!unlink` – stop saving what you send from this chat\n" +
	"• `!remind WHEN` as a reply to a message – remind you about that note, e.g. `!remind tomorrow 9am`\n" +
	"• `!remind WHEN TEXT` – save TEXT as a note and remind you, e.g. `!remind in 2 hours call the dentist`\n" +
	"• `!snooze DURATION` as a reply to a reminder – e.g. `!snooze 30m` or `!snooze tomorrow`\n" +
	"• `!done` as a reply to a reminder – mark it done\n" +
	"• `!help` – this message"

func reply(ok bool, text string) CommandOutcome {
	return CommandOutcome{OK: ok, Feedback: Feedback{ReplyText: &text}}
}

// HandleCommand runs a command for a sender. `link` and `help` work for unlinked senders; every
// other command from an unlinked sender is answered like any message from one (AUTH-B6).
func (s *Service) HandleCommand(ctx context.Context, bot *bots.Principal, cmd Command) (CommandOutcome, error) {
	if !storable(cmd.Name, cmd.Sender, cmd.Conversation, cmd.ReplyTo, cmd.MessageID) {
		return CommandOutcome{}, fmt.Errorf("%w: identifier is not valid text", ErrInvalid)
	}
	cmd.Args = cleanText(cmd.Args)
	name := strings.ToLower(strings.TrimSpace(cmd.Name))
	if cmd.Sender == "" || cmd.Conversation == "" || name == "" {
		return CommandOutcome{}, fmt.Errorf("%w: sender, conversation and command are required", ErrInvalid)
	}
	switch name {
	case "help":
		return reply(true, helpText), nil
	case "link":
		return s.link(ctx, bot, cmd)
	}
	ident, ok, err := s.Bots.ResolveIdentity(ctx, bot, cmd.Sender)
	if err != nil {
		return CommandOutcome{}, err
	}
	if !ok {
		out := CommandOutcome{}
		if due, err := s.Bots.UnlinkNoticeDue(ctx, bot, cmd.Sender); err == nil && due {
			r := unlinkedReply
			out.Feedback.ReplyText = &r
		}
		return out, nil
	}
	switch name {
	case "unlink":
		if err := s.Bots.Unlink(ctx, store.Actor{Kind: "user", ID: &ident.UserID}, ident.UserID, ident.ID); err != nil {
			return CommandOutcome{}, err
		}
		return reply(true, "Unlinked. I won't save anything you send from this chat until you link it again."), nil
	case "remind":
		return s.remind(ctx, bot, ident, cmd)
	case "snooze", "done":
		return s.answerReminder(ctx, bot, ident, name, cmd)
	default:
		return reply(false, fmt.Sprintf("I don't know the command `!%s`. Send `!help` to see what I understand.", name)), nil
	}
}

func (s *Service) link(ctx context.Context, bot *bots.Principal, cmd Command) (CommandOutcome, error) {
	if strings.TrimSpace(cmd.Args) == "" {
		return reply(false, "Send the code like this: `!link ABCD-1234`. You can create one in the app under Settings → Chats."), nil
	}
	outcome, _, err := s.Bots.Link(ctx, bot, cmd.Sender, cmd.Conversation, cmd.Args)
	if err != nil {
		return CommandOutcome{}, err
	}
	switch outcome {
	case bots.Linked:
		return reply(true, "Linked to your account. Everything you send me from now on becomes a note in your Inbox."), nil
	case bots.LinkAlready:
		return reply(false, "This chat is already linked to an account. Send `!unlink` first if you want to link it somewhere else."), nil
	case bots.LinkThrottled:
		return reply(false, "Too many attempts. Please wait a few minutes and try again."), nil
	default: // invalid code, wrong domain: deliberately the same answer
		return reply(false, "That code is not valid or has expired. Create a new one in the app under Settings → Chats."), nil
	}
}
