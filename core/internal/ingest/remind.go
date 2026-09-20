package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/reminders"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
	"github.com/Niboor/notekeeper/core/internal/timeparse"
)

const remindUsage = "Try `!remind tomorrow 9am` as a reply to a message, or `!remind in 2 hours call the dentist` to make a new note."

func (s *Service) userZone(ctx context.Context, q *dbq.Queries, user uuid.UUID) *time.Location {
	u, err := q.GetUser(ctx, user)
	if err != nil {
		return time.UTC
	}
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// clock is the time a command counts as being sent: the platform's timestamp, unless it is
// missing or absurd (CORE-N18).
func (s *Service) clock(cmd Command) time.Time {
	now := s.Now()
	if cmd.Timestamp.IsZero() || cmd.Timestamp.After(now.Add(maxFutureSkew)) {
		return now
	}
	return cmd.Timestamp
}

// once runs a command that changes something at most once per chat message: the first run records its
// outcome under the message id, and a replay of the same message (the bot crashed before it committed
// its sync token, or the batch was replayed) answers with that outcome and changes nothing (BOT-7,
// CR-010). A run that is rolled back leaves no record, so it can be tried again.
func (s *Service) once(ctx context.Context, tx *store.UserTx, bot *bots.Principal, kind string, cmd Command, out *CommandOutcome, run func() error) error {
	if cmd.MessageID == "" {
		return run()
	}
	id := kind + ":" + cmd.MessageID
	n, err := tx.Q.InsertIngestEvent(ctx, dbq.InsertIngestEventParams{BotInstanceID: bot.InstanceID, EventID: id, UserID: tx.UserID, Result: []byte(`{}`)})
	if err != nil {
		return err
	}
	if n == 0 {
		raw, err := tx.Q.GetIngestEvent(ctx, dbq.GetIngestEventParams{BotInstanceID: bot.InstanceID, EventID: id, UserID: tx.UserID})
		if err != nil {
			return err
		}
		_ = json.Unmarshal(raw, out)
		return nil
	}
	if err := run(); err != nil {
		return err
	}
	raw, _ := json.Marshal(out)
	return tx.Q.UpdateIngestResult(ctx, dbq.UpdateIngestResultParams{BotInstanceID: bot.InstanceID, EventID: id, Result: raw, UserID: tx.UserID})
}

func timeProblem(err error, what string) CommandOutcome {
	if errors.Is(err, timeparse.ErrPast) {
		return reply(false, "That time has already passed. "+remindUsage)
	}
	return reply(false, fmt.Sprintf("I could not understand %s. Try %s.", what, timeparse.Example))
}

// remind handles `!remind <when>` as a reply (a reminder on that message's note) and
// `!remind <when> <text>` (a new note with a reminder), CORE-R11a and CORE-R11b. Ordinary messages
// never create reminders; only this command does. Everything happens in one transaction, so a
// failed reminder leaves no note behind, and a replayed command changes nothing (BOT-7).
func (s *Service) remind(ctx context.Context, bot *bots.Principal, ident bots.Identity, cmd Command) (CommandOutcome, error) {
	if strings.TrimSpace(cmd.Args) == "" {
		return reply(false, "When should I remind you? "+remindUsage), nil
	}
	var out CommandOutcome
	err := s.St.InUserTx(ctx, ident.UserID, func(tx *store.UserTx) error {
		if tx.Status != "active" {
			out = reply(false, "This account is not active.")
			return nil
		}
		return s.once(ctx, tx, bot, "remind", cmd, &out, func() error {
			loc := s.userZone(ctx, tx.Q, tx.UserID)
			now := s.clock(cmd)
			when, text, err := timeparse.SplitWhen(cmd.Args, now, loc)
			if err != nil {
				out = timeProblem(err, "that time")
				return nil
			}
			if cmd.ReplyTo != "" { // a reminder on the note of the message replied to
				if text != "" {
					out = reply(false, "Reply with just the time, for example `!remind tomorrow 9am`. To make a new note, send `!remind` and the time and text without replying.")
					return nil
				}
				parts, err := tx.Q.PartsBySourceMessage(ctx, dbq.PartsBySourceMessageParams{UserID: tx.UserID, SourceBotInstanceID: uuid.NullUUID{UUID: bot.InstanceID, Valid: true},
					SourceConversationID: &cmd.Conversation, SourceMessageID: &cmd.ReplyTo})
				if err != nil {
					return err
				}
				if len(parts) == 0 {
					out = reply(false, "I can only set a reminder on a message that became a note, and I do not have that one.")
					return nil
				}
				r, err := s.Reminders.CreateIn(ctx, tx, parts[0].NoteID, when, nil)
				if err != nil {
					return s.remindFailure(err, &out)
				}
				out = CommandOutcome{OK: true, Feedback: Feedback{React: "ok", ReplyText: ptr(fmt.Sprintf("⏰ I will remind you about that note on %s.", timeparse.Describe(r.DueAt, loc)))}}
				return nil
			}
			if text == "" {
				out = reply(false, "What should I remind you about? Send the time and the text together, or reply to a message. "+remindUsage)
				return nil
			}
			ev := Event{EventID: "remind:" + cmd.MessageID, Kind: KindCreated, Sender: cmd.Sender, Conversation: cmd.Conversation, MessageID: cmd.MessageID,
				Timestamp: now, Parts: []Part{{Type: PartText, Text: text}}, Standalone: true}
			// The note does not carry the command message as its source. Editing that message in the chat
			// would otherwise replace the note's text with the raw command line and leave the reminder where it
			// was; with a source of its own, such an edit finds nothing and is ignored (EDT-6). The note and
			// the reminder are then changed in the app.
			ev.MessageID = "remind:" + cmd.MessageID
			if cmd.MessageID == "" {
				ev.MessageID = "remind:" + uuid.NewString()
			}
			if err := ev.validate(); err != nil {
				out = reply(false, "That text is not valid: "+err.Error())
				return nil
			}
			created, err := s.created(ctx, tx, bot, ident, ev, s.Now())
			if err != nil {
				return err
			}
			r, err := s.Reminders.CreateIn(ctx, tx, *created.NoteID, when, nil)
			if err != nil {
				return s.remindFailure(err, &out)
			}
			out = CommandOutcome{OK: true, Feedback: Feedback{React: "ok", ReplyText: ptr(fmt.Sprintf("⏰ Saved as a note. I will remind you on %s.", timeparse.Describe(r.DueAt, loc)))}}
			return nil
		})
	})
	if errors.Is(err, errRollback) {
		err = nil // the reply was set; nothing was saved
	}
	if le := (*store.LimitError)(nil); errors.As(err, &le) {
		return reply(false, limitReply(le)), nil
	}
	return out, err
}

// remindFailure turns a refusal into a reply and rolls the transaction back by returning errAbortCommand.
func (s *Service) remindFailure(err error, out *CommandOutcome) error {
	switch {
	case errors.Is(err, reminders.ErrPast):
		*out = timeProblem(timeparse.ErrPast, "")
	case errors.Is(err, reminders.ErrLimit):
		*out = reply(false, "You have too many reminders. Clear some in the app first.")
	case errors.Is(err, reminders.ErrNotActive):
		*out = reply(false, "That note is in the Trash. Restore it first.")
	case errors.Is(err, reminders.ErrInvalid):
		*out = reply(false, "That time is too far ahead. "+remindUsage)
	default:
		return err
	}
	return errRollback
}

var errRollback = errors.New("roll back")

// answerReminder handles `!snooze` and `!done` as a reply to a reminder message (CORE-R11c). The
// message is resolved through the chat messages the bot reported for its deliveries, so it can only
// ever name a reminder that was sent to this identity's own conversation (SEC-BOT-11).
func (s *Service) answerReminder(ctx context.Context, bot *bots.Principal, ident bots.Identity, name string, cmd Command) (CommandOutcome, error) {
	if cmd.ReplyTo == "" {
		if name == "snooze" {
			return reply(false, "Reply to a reminder message with `!snooze 30m` (or `!snooze tomorrow`)."), nil
		}
		return reply(false, "Reply to a reminder message with `!done`."), nil
	}
	var out CommandOutcome
	err := s.St.InUserTx(ctx, ident.UserID, func(tx *store.UserTx) error {
		if ident.ConversationID == nil || *ident.ConversationID != cmd.Conversation {
			out = reply(false, "I can only do that in the chat I send your reminders to.")
			return nil
		}
		id, ok, err := s.Reminders.ForChatMessage(ctx, tx, bot.InstanceID, cmd.Conversation, cmd.ReplyTo)
		if err != nil {
			return err
		}
		if !ok {
			out = reply(false, "That is not one of my reminder messages. Reply to the reminder itself.")
			return nil
		}
		return s.once(ctx, tx, bot, name, cmd, &out, func() error {
			loc := s.userZone(ctx, tx.Q, tx.UserID)
			if name == "done" {
				r, err := s.Reminders.DoneIn(ctx, tx, id)
				if errors.Is(err, reminders.ErrNotFound) {
					out = reply(false, "That reminder is no longer there.")
					return nil
				}
				if err != nil {
					return err
				}
				text := "✅ Marked done."
				if r.State == "pending" {
					text = "✅ Skipped this time. Next reminder: " + timeparse.Describe(r.DueAt, loc) + "."
				}
				out = CommandOutcome{OK: true, Feedback: Feedback{ReplyText: &text}}
				return nil
			}
			if strings.TrimSpace(cmd.Args) == "" {
				out = reply(false, "For how long? For example `!snooze 30m`, `!snooze 2 hours` or `!snooze tomorrow`.")
				return nil
			}
			until, err := timeparse.ParseSnooze(cmd.Args, s.clock(cmd), loc)
			if err != nil {
				out = timeProblem(err, "how long to snooze")
				return nil
			}
			r, err := s.Reminders.SnoozeIn(ctx, tx, id, until)
			if errors.Is(err, reminders.ErrNotFound) {
				out = reply(false, "That reminder is no longer there.")
				return nil
			}
			if errors.Is(err, reminders.ErrPast) {
				out = timeProblem(timeparse.ErrPast, "")
				return nil
			}
			if err != nil {
				return err
			}
			text := "💤 Snoozed until " + timeparse.Describe(r.DueAt, loc) + "."
			out = CommandOutcome{OK: true, Feedback: Feedback{ReplyText: &text}}
			return nil
		})
	})
	return out, err
}

func ptr(s string) *string { return &s }
