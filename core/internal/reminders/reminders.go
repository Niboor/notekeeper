// Package reminders implements reminders on notes (docs/design/05-realtime-and-jobs.md section 5,
// requirements CORE-R1..R11). A reminder is an absolute instant, optionally repeating. Firing holds
// no in-process timers: a periodic job claims due reminders with a lease, and each fires in its own
// transaction that first locks the owner, so any replica can do it and edits win races (CORE-R5).
package reminders

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/notify"
	"github.com/Niboor/notekeeper/core/internal/obs"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
	"github.com/Niboor/notekeeper/core/internal/timeparse"
)

// Errors.
var (
	ErrNotFound  = store.ErrNotFound
	ErrInvalid   = errors.New("invalid reminder")
	ErrPast      = errors.New("the reminder time is in the past")
	ErrNotActive = errors.New("a note in the Trash cannot have reminders")
	ErrLimit     = errors.New("too many reminders")
)

// Limits.
const (
	MaxPerNote = 20
	MaxPerUser = 500
	// LateAfter is how far past due a delivery must be to be flagged late (CORE-R4).
	LateAfter = 5 * time.Minute
	// Lease is how long a claimed reminder is reserved for the firing transaction (docs/design/05 5.1).
	Lease = 60 * time.Second
)

// Config holds what the service needs to build messages.
type Config struct {
	AppURL string // public address of the web app, for the deep link in a reminder
}

// Service manages reminders.
type Service struct {
	St  *store.Store
	Cfg Config
	Now func() time.Time
}

// New creates the service.
func New(st *store.Store, cfg Config) *Service { return &Service{St: st, Cfg: cfg, Now: time.Now} }

func (s *Service) validate(due time.Time, rrule *string, now time.Time) error {
	if !due.After(now) {
		return ErrPast
	}
	if due.After(now.AddDate(10, 0, 0)) {
		return ErrInvalid
	}
	if rrule != nil {
		if _, err := timeparse.ParseRule(*rrule); err != nil {
			return ErrInvalid
		}
	}
	return nil
}

func userZone(ctx context.Context, q *dbq.Queries, user uuid.UUID) (string, *time.Location, error) {
	u, err := q.GetUser(ctx, user)
	if err != nil {
		return "", nil, err
	}
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return "UTC", time.UTC, nil
	}
	return u.Timezone, loc, nil
}

// Create adds a reminder to one of the user's notes (CORE-R1).
func (s *Service) Create(ctx context.Context, user, note uuid.UUID, due time.Time, rrule *string) (dbq.Reminder, error) {
	var out dbq.Reminder
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		var err error
		out, err = s.CreateIn(ctx, tx, note, due, rrule)
		return err
	})
	return out, err
}

// CreateIn is Create inside the caller's transaction (used by the chat commands).
func (s *Service) CreateIn(ctx context.Context, tx *store.UserTx, note uuid.UUID, due time.Time, rrule *string) (dbq.Reminder, error) {
	now := s.Now()
	if err := s.validate(due, rrule, now); err != nil {
		return dbq.Reminder{}, err
	}
	n, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: note, UserID: tx.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.Reminder{}, ErrNotFound
	}
	if err != nil {
		return dbq.Reminder{}, err
	}
	if n.State != "active" {
		return dbq.Reminder{}, ErrNotActive
	}
	perNote, err := tx.Q.CountNoteReminders(ctx, dbq.CountNoteRemindersParams{UserID: tx.UserID, NoteID: note})
	if err != nil {
		return dbq.Reminder{}, err
	}
	perUser, err := tx.Q.CountUserReminders(ctx, tx.UserID)
	if err != nil {
		return dbq.Reminder{}, err
	}
	if perNote >= MaxPerNote || perUser >= MaxPerUser {
		return dbq.Reminder{}, ErrLimit
	}
	tz, _, err := userZone(ctx, tx.Q, tx.UserID)
	if err != nil {
		return dbq.Reminder{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbq.Reminder{}, err
	}
	r, err := tx.Q.InsertReminder(ctx, dbq.InsertReminderParams{ID: id, UserID: tx.UserID, NoteID: note, DueAt: due, Rrule: rrule, Tz: tz, CreatedAt: now})
	if err != nil {
		return dbq.Reminder{}, err
	}
	return r, s.changed(ctx, tx, r)
}

func (s *Service) changed(ctx context.Context, tx *store.UserTx, r dbq.Reminder) error {
	if err := tx.Change(ctx, "reminder", r.ID, "upsert", &r.Version); err != nil {
		return err
	}
	return tx.Change(ctx, "note", r.NoteID, "upsert", nil) // the note's reminders are part of what its views show
}

// Update changes the time or the recurrence of a reminder and arms it again (CORE-R1, CORE-R10).
// setRule says whether rrule is meant (a nil rrule then clears the recurrence).
func (s *Service) Update(ctx context.Context, user, id uuid.UUID, due *time.Time, setRule bool, rrule *string) (dbq.Reminder, error) {
	var out dbq.Reminder
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		cur, err := tx.Q.GetReminder(ctx, dbq.GetReminderParams{ID: id, UserID: user})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		newDue, newRule := cur.DueAt, cur.Rrule
		if due != nil {
			newDue = *due
		}
		if setRule {
			newRule = rrule
		}
		if err := s.validate(newDue, newRule, s.Now()); err != nil {
			return err
		}
		n, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: cur.NoteID, UserID: user})
		if err != nil {
			return err
		}
		if n.State != "active" {
			return ErrNotActive
		}
		out, err = tx.Q.SetReminder(ctx, dbq.SetReminderParams{ID: id, UserID: user, DueAt: newDue, Rrule: newRule, Tz: cur.Tz})
		if err != nil {
			return err
		}
		return s.changed(ctx, tx, out)
	})
	return out, err
}

// Delete clears a reminder; for a recurring one this ends it (CORE-R1, CORE-R10).
func (s *Service) Delete(ctx context.Context, user, id uuid.UUID) error {
	return s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		note, err := tx.Q.DeleteReminder(ctx, dbq.DeleteReminderParams{ID: id, UserID: user})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := tx.Change(ctx, "reminder", id, "delete", nil); err != nil {
			return err
		}
		return tx.Change(ctx, "note", note, "upsert", nil)
	})
}

// Snooze moves a reminder to a later time and arms it again (CORE-R8).
func (s *Service) Snooze(ctx context.Context, user, id uuid.UUID, until time.Time) (dbq.Reminder, error) {
	var out dbq.Reminder
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		var err error
		out, err = s.SnoozeIn(ctx, tx, id, until)
		return err
	})
	return out, err
}

// SnoozeIn is Snooze inside the caller's transaction.
func (s *Service) SnoozeIn(ctx context.Context, tx *store.UserTx, id uuid.UUID, until time.Time) (dbq.Reminder, error) {
	if !until.After(s.Now()) || until.After(s.Now().AddDate(1, 0, 0)) {
		return dbq.Reminder{}, ErrPast
	}
	r, err := tx.Q.SnoozeReminder(ctx, dbq.SnoozeReminderParams{ID: id, UserID: tx.UserID, DueAt: until})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.Reminder{}, ErrNotFound
	}
	if err != nil {
		return dbq.Reminder{}, err
	}
	return r, s.changed(ctx, tx, r)
}

// Done marks a reminder done (CORE-R8). A recurring reminder skips this occurrence and stays armed
// for the next one (CORE-R10).
func (s *Service) Done(ctx context.Context, user, id uuid.UUID) (dbq.Reminder, error) {
	var out dbq.Reminder
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		var err error
		out, err = s.DoneIn(ctx, tx, id)
		return err
	})
	return out, err
}

// DoneIn is Done inside the caller's transaction.
func (s *Service) DoneIn(ctx context.Context, tx *store.UserTx, id uuid.UUID) (dbq.Reminder, error) {
	cur, err := tx.Q.GetReminder(ctx, dbq.GetReminderParams{ID: id, UserID: tx.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.Reminder{}, ErrNotFound
	}
	if err != nil {
		return dbq.Reminder{}, err
	}
	if cur.Rrule != nil && (cur.State == "pending" || cur.State == "fired") {
		_, loc, err := userZone(ctx, tx.Q, tx.UserID)
		if err != nil {
			return dbq.Reminder{}, err
		}
		if rule, err := timeparse.ParseRule(*cur.Rrule); err == nil {
			if next := rule.Next(cur.DueAt, s.Now(), loc); !next.IsZero() {
				r, err := tx.Q.SnoozeReminder(ctx, dbq.SnoozeReminderParams{ID: id, UserID: tx.UserID, DueAt: next})
				if err != nil {
					return dbq.Reminder{}, err
				}
				return r, s.changed(ctx, tx, r)
			}
		}
	}
	r, err := tx.Q.FinishReminder(ctx, dbq.FinishReminderParams{ID: id, UserID: tx.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.Reminder{}, ErrNotFound
	}
	if err != nil {
		return dbq.Reminder{}, err
	}
	return r, s.changed(ctx, tx, r)
}

// Upcoming lists the user's armed reminders, soonest first (CORE-R9).
func (s *Service) Upcoming(ctx context.Context, user uuid.UUID) ([]dbq.ListUpcomingRemindersRow, error) {
	var out []dbq.ListUpcomingRemindersRow
	err := s.St.InUserRead(ctx, user, func(q *dbq.Queries) error {
		var err error
		out, err = q.ListUpcomingReminders(ctx, user)
		return err
	})
	return out, err
}

// ForChatMessage finds the reminder a chat message was sent for, so `!snooze` and `!done` as replies
// can act on it (CORE-R11c). Only the identity's own conversation can resolve it.
func (s *Service) ForChatMessage(ctx context.Context, tx *store.UserTx, bot uuid.UUID, conversation, message string) (uuid.UUID, bool, error) {
	id, err := tx.Q.ReminderByChatMessage(ctx, dbq.ReminderByChatMessageParams{BotInstanceID: bot, ConversationID: conversation, MessageID: message, UserID: uuid.NullUUID{UUID: tx.UserID, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil || !id.Valid {
		return uuid.Nil, false, err
	}
	return id.UUID, true, nil
}

// OnDismiss suspends a note's pending reminders (CORE-R7). Called inside the dismissal transaction.
func OnDismiss(ctx context.Context, tx *store.UserTx, note uuid.UUID) error {
	ids, err := tx.Q.SuspendNoteReminders(ctx, dbq.SuspendNoteRemindersParams{UserID: tx.UserID, NoteID: note})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := tx.Change(ctx, "reminder", id, "upsert", nil); err != nil {
			return err
		}
	}
	return nil
}

// OnRestore re-arms the reminders that were suspended with the note and are still in the future; the
// ones that came due meanwhile are cancelled without being sent, and a recurring one moves to its
// next occurrence (CORE-R7).
func OnRestore(ctx context.Context, tx *store.UserTx, note uuid.UUID, now time.Time) error {
	list, err := tx.Q.SuspendedNoteReminders(ctx, dbq.SuspendedNoteRemindersParams{UserID: tx.UserID, NoteID: note})
	if err != nil {
		return err
	}
	_, loc, err := userZone(ctx, tx.Q, tx.UserID)
	if err != nil {
		return err
	}
	for _, r := range list {
		state, due := "pending", r.DueAt
		if !r.DueAt.After(now) {
			state = "cancelled"
			if r.Rrule != nil {
				if rule, err := timeparse.ParseRule(*r.Rrule); err == nil {
					if next := rule.Next(r.DueAt, now, loc); !next.IsZero() {
						state, due = "pending", next
					}
				}
			}
		}
		if err := tx.Q.RestoreReminder(ctx, dbq.RestoreReminderParams{ID: r.ID, State: state, DueAt: due}); err != nil {
			return err
		}
		if err := tx.Change(ctx, "reminder", r.ID, "upsert", nil); err != nil {
			return err
		}
	}
	return nil
}

// ---- firing -------------------------------------------------------------------------------------

// FireDue fires the reminders that are due and returns how many it fired (CORE-R3, CORE-R5).
func (s *Service) FireDue(ctx context.Context) (int, error) {
	now := s.Now()
	var claimed []dbq.ClaimDueRemindersRow
	err := s.St.InSchedulerTx(ctx, func(q *dbq.Queries) error {
		var err error
		claimed, err = q.ClaimDueReminders(ctx, dbq.ClaimDueRemindersParams{Lease: now.Add(Lease), Now: now})
		return err
	})
	if err != nil {
		return 0, err
	}
	fired := 0
	for _, c := range claimed {
		ok, err := s.fireOne(ctx, c.UserID, c.ID)
		if err != nil {
			return fired, err
		}
		if ok {
			fired++
		}
	}
	return fired, nil
}

// fireOne fires one claimed reminder in a transaction that first locks the owner and then re-reads
// the reminder: if the user changed it meanwhile, it no longer qualifies and nothing is sent.
func (s *Service) fireOne(ctx context.Context, user, id uuid.UUID) (bool, error) {
	fired := false
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		now := s.Now()
		if tx.Status != "active" {
			return nil
		}
		r, err := tx.Q.GetDueReminder(ctx, dbq.GetDueReminderParams{ID: id, UserID: user, DueAt: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		n, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: r.NoteID, UserID: user})
		if err != nil || n.State != "active" {
			return err
		}
		excerpt, err := tx.Q.NoteExcerpt(ctx, dbq.NoteExcerptParams{UserID: user, NoteID: r.NoteID})
		if err != nil {
			return err
		}
		atts, err := tx.Q.NoteAttachmentInfo(ctx, dbq.NoteAttachmentInfoParams{UserID: user, NoteID: r.NoteID})
		if err != nil {
			return err
		}
		_, loc, err := userZone(ctx, tx.Q, user)
		if err != nil {
			return err
		}
		late := now.Sub(r.DueAt) > LateAfter
		obs.RemindersFired.WithLabelValues(map[bool]string{true: "true", false: "false"}[late]).Inc()
		obs.ReminderLag.Observe(max(0, now.Sub(r.DueAt).Seconds()))
		link := strings.TrimRight(s.Cfg.AppURL, "/") + "/notes/" + r.NoteID.String()
		text := Message(excerpt, link, r.DueAt, late, loc, len(atts))

		attachments := make([]map[string]any, len(atts))
		for i, a := range atts {
			attachments[i] = map[string]any{"id": a.ID, "filename": a.Filename, "media_type": a.MediaType, "size": a.SizeBytes}
		}
		payload, err := json.Marshal(map[string]any{"text": text, "note_url": link, "late": late, "attachments": attachments})
		if err != nil {
			return err
		}
		targets, err := tx.Q.ReminderTargets(ctx, user)
		if err != nil {
			return err
		}
		instances := map[uuid.UUID]bool{}
		for _, t := range targets {
			oid, err := uuid.NewV7()
			if err != nil {
				return err
			}
			if err := tx.Q.InsertReminderOutbox(ctx, dbq.InsertReminderOutboxParams{ID: oid, BotInstanceID: t.BotInstanceID, UserID: uuid.NullUUID{UUID: user, Valid: true},
				ExternalUserID: t.ExternalUserID, ConversationID: *t.ConversationID, Payload: payload, NextAttemptAt: now, DueAt: &r.DueAt,
				ReminderID: uuid.NullUUID{UUID: r.ID, Valid: true}}); err != nil {
				return err
			}
			instances[t.BotInstanceID] = true
		}
		for inst := range instances {
			if err := tx.Notify(ctx, store.ChannelOutbox, inst.String()); err != nil {
				return err
			}
		}
		np, _ := json.Marshal(map[string]any{"reminder_id": r.ID, "note_id": r.NoteID, "excerpt": Excerpt(excerpt, 140), "due_at": r.DueAt, "late": late})
		nid, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if err := tx.Q.InsertNotification(ctx, dbq.InsertNotificationParams{ID: nid, UserID: user, Kind: notify.KindReminder, Payload: np}); err != nil {
			return err
		}
		if err := tx.Change(ctx, "notification", nid, "upsert", nil); err != nil {
			return err
		}
		// One-off: fired, and it stays on the note. Recurring: the next occurrence after now; whatever
		// was missed during an outage collapses into this one late delivery (CORE-R10).
		if r.Rrule != nil {
			if rule, err := timeparse.ParseRule(*r.Rrule); err == nil {
				if next := rule.Next(r.DueAt, now, loc); !next.IsZero() {
					if err := tx.Q.AdvanceReminder(ctx, dbq.AdvanceReminderParams{ID: r.ID, DueAt: next, LastFiredAt: &now}); err != nil {
						return err
					}
					fired = true
					return s.changed(ctx, tx, r)
				}
			}
		}
		if err := tx.Q.MarkReminderFired(ctx, dbq.MarkReminderFiredParams{ID: r.ID, LastFiredAt: &now}); err != nil {
			return err
		}
		fired = true
		return s.changed(ctx, tx, r)
	})
	return fired, err
}

// Excerpt shortens text to n characters for a preview.
func Excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}

// Message renders a reminder for a chat: the note text (shortened), when it was due if it is late,
// how many files follow, and the deep link (CORE-R6). The text is inert content: the bot sends it as
// plain text, so nothing in a note can mention anyone or run a command.
func Message(excerpt, link string, due time.Time, late bool, loc *time.Location, files int) string {
	var b strings.Builder
	b.WriteString("⏰ Reminder")
	if late {
		b.WriteString(" (late, it was due " + timeparse.Describe(due, loc) + ")")
	}
	b.WriteString("\n")
	if strings.TrimSpace(excerpt) == "" {
		b.WriteString("(a note without text)")
	} else {
		b.WriteString(Excerpt(excerpt, 300))
	}
	if files > 0 {
		b.WriteString("\n📎 " + plural(files, "file") + " attached")
	}
	b.WriteString("\n" + link)
	return b.String()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return itoa(n) + " " + word + "s"
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
