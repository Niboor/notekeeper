// Package ingest turns normalised chat events into notes (docs/design/04-ingestion.md). Bots
// forward events; everything they mean is decided here, in one transaction per event that
// first locks the user's row, so concurrent deliveries can never both decide "new note".
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Niboor/notekeeper/core/internal/blobs"
	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/grouping"
	"github.com/Niboor/notekeeper/core/internal/obs"
	"github.com/Niboor/notekeeper/core/internal/reminders"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// Limits on what one event may carry (SEC-API-3).
const (
	MaxPartsPerEvent = 20
	MaxTextLength    = 100_000 // characters per text part
	MaxFieldLength   = 512     // ids, filenames, descriptions
	maxFutureSkew    = 5 * time.Minute
)

// Event kinds.
const (
	KindCreated = "message_created"
	KindEdited  = "message_edited"
	KindDeleted = "message_deleted"
)

// Part types of an event.
const (
	PartText             = "text"
	PartAttachment       = "attachment"
	PartAttachmentFailed = "attachment_failed"
	PartUnsupported      = "unsupported"
)

// Event is a normalised chat event as posted by a bot.
type Event struct {
	EventID      string
	Kind         string
	Sender       string
	Conversation string
	MessageID    string
	Timestamp    time.Time
	ReplyTo      string
	Thread       string
	Parts        []Part
	// Standalone always starts a new note, whatever came just before (a note made by a command).
	Standalone bool
}

// Part is one piece of an event.
type Part struct {
	Type        string
	Text        string
	UploadID    *uuid.UUID
	Filename    string
	MediaType   string
	Size        int64
	Reason      string
	Description string
}

// Result codes.
const (
	ResultCreated  = "created"
	ResultAppended = "appended"
	ResultUpdated  = "updated"
	ResultRemoved  = "removed"
	ResultIgnored  = "ignored"
	ResultRejected = "rejected"
)

// Rejection codes.
const (
	CodeIdentityUnlinked = "identity_unlinked"
	CodeInvalid          = "invalid_event"
	CodeUserInactive     = "user_inactive"
	CodeBeforeLink       = "before_link"
)

// Feedback tells the bot what to show, so bots contain no wording of their own (BOT-8).
type Feedback struct {
	React     string  `json:"react,omitempty"`
	ReplyText *string `json:"reply_text,omitempty"`
}

// Outcome is the result of handling one event. It is stored with the event id, so a replayed
// event gets the same answer (BOT-7).
type Outcome struct {
	Result   string     `json:"result"`
	NoteID   *uuid.UUID `json:"note_id,omitempty"`
	Code     string     `json:"code,omitempty"`
	Feedback Feedback   `json:"feedback"`
}

// ErrInvalid is returned for a malformed event (HTTP 400); the bot must not retry it.
var ErrInvalid = errors.New("invalid event")

// Service ingests events.
type Service struct {
	St        *store.Store
	Bots      *bots.Service
	Blobs     *blobs.Service
	Reminders *reminders.Service
	Now       func() time.Time
	// DefaultWindow is the grouping window when a user has not set one (GRP-5).
	DefaultWindow time.Duration
}

// New creates the service.
func New(st *store.Store, b *bots.Service, bl *blobs.Service, rem *reminders.Service) *Service {
	return &Service{St: st, Bots: b, Blobs: bl, Reminders: rem, Now: time.Now, DefaultWindow: time.Minute}
}

// window returns the grouping window of a user: their setting, else the deployment default.
func (s *Service) window(settings []byte) time.Duration {
	var cfg struct {
		Seconds *int `json:"grouping_window_seconds"`
	}
	if err := json.Unmarshal(settings, &cfg); err == nil && cfg.Seconds != nil && *cfg.Seconds >= 0 && *cfg.Seconds <= 3600 {
		return time.Duration(*cfg.Seconds) * time.Second
	}
	return s.DefaultWindow
}

func (e Event) validate() error {
	bad := func(msg string) error { return fmt.Errorf("%w: %s", ErrInvalid, msg) }
	if e.EventID == "" || e.Sender == "" || e.Conversation == "" || e.MessageID == "" {
		return bad("event_id, sender, conversation and message_id are required")
	}
	for _, f := range []string{e.EventID, e.Sender, e.Conversation, e.MessageID, e.ReplyTo, e.Thread} {
		if len(f) > MaxFieldLength {
			return bad("identifier too long")
		}
	}
	if e.Timestamp.IsZero() {
		return bad("timestamp is required")
	}
	switch e.Kind {
	case KindCreated, KindEdited:
		if len(e.Parts) == 0 {
			return bad("no parts")
		}
	case KindDeleted:
	default:
		return bad("unknown kind")
	}
	if len(e.Parts) > MaxPartsPerEvent {
		return bad("too many parts")
	}
	for _, p := range e.Parts {
		switch p.Type {
		case PartText:
			if p.Text == "" || utf8.RuneCountInString(p.Text) > MaxTextLength {
				return bad("text part empty or too long")
			}
		case PartUnsupported:
			if len(p.Description) > MaxFieldLength {
				return bad("description too long")
			}
		case PartAttachment, PartAttachmentFailed:
			if len(p.Filename) > MaxFieldLength || len(p.MediaType) > MaxFieldLength {
				return bad("filename too long")
			}
		default:
			return bad("unknown part type")
		}
	}
	return nil
}

const unlinkedReply = "I don't know you yet. To link this chat to your Notekeeper account, create a code under Settings → Chats in the app and send it here as `!link CODE`."

// Handle processes one event from an authenticated bot.
func (s *Service) Handle(ctx context.Context, bot *bots.Principal, ev Event) (Outcome, error) {
	if err := ev.validate(); err != nil {
		return Outcome{}, err
	}
	ident, ok, err := s.Bots.ResolveIdentity(ctx, bot, ev.Sender)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		out := Outcome{Result: ResultRejected, Code: CodeIdentityUnlinked}
		obs.Ingest.WithLabelValues(ev.Kind, ResultRejected).Inc()
		if due, err := s.Bots.UnlinkNoticeDue(ctx, bot, ev.Sender); err == nil && due {
			reply := unlinkedReply
			out.Feedback.ReplyText = &reply
		}
		return out, nil // nothing from the message is stored
	}

	var out Outcome
	err = s.St.InUserTx(ctx, ident.UserID, func(tx *store.UserTx) error {
		if tx.Status != "active" {
			out = Outcome{Result: ResultRejected, Code: CodeUserInactive}
			return nil
		}
		// The identity may have been unlinked while we waited for the lock.
		cur, err := tx.Q.GetIdentity(ctx, ident.ID)
		if err != nil || cur.UserID != ident.UserID {
			out = Outcome{Result: ResultRejected, Code: CodeIdentityUnlinked}
			return nil
		}
		// Idempotent replay (BOT-7): the second delivery of an event gets the first answer.
		stored, err := json.Marshal(Outcome{})
		if err != nil {
			return err
		}
		n, err := tx.Q.InsertIngestEvent(ctx, dbq.InsertIngestEventParams{BotInstanceID: bot.InstanceID, EventID: ev.EventID, UserID: tx.UserID, Result: stored})
		if err != nil {
			return err
		}
		if n == 0 {
			raw, err := tx.Q.GetIngestEvent(ctx, dbq.GetIngestEventParams{BotInstanceID: bot.InstanceID, EventID: ev.EventID, UserID: tx.UserID})
			if errors.Is(err, pgx.ErrNoRows) { // the id was used for another user's message: never answer with their outcome
				return fmt.Errorf("%w: event_id already used", ErrInvalid)
			}
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, &out)
		}
		if err := tx.Q.UpdateIdentityConversation(ctx, dbq.UpdateIdentityConversationParams{ID: ident.ID, ConversationID: &ev.Conversation}); err != nil {
			return err
		}
		now := s.Now()
		if ev.Timestamp.Before(ident.LinkedAt) {
			out = Outcome{Result: ResultIgnored, Code: CodeBeforeLink} // MX-10: nothing from before linking
		} else {
			if ev.Timestamp.After(now.Add(maxFutureSkew)) {
				ev.Timestamp = now // CORE-N18: a clock in the future must not pin a note to the top
			}
			switch ev.Kind {
			case KindCreated:
				out, err = s.created(ctx, tx, bot, ident, ev, now)
			case KindEdited:
				out, err = s.edited(ctx, tx, bot, ident, ev, now)
			default:
				out, err = s.deleted(ctx, tx, bot, ident, ev, now)
			}
			if err != nil {
				return err
			}
		}
		obs.Ingest.WithLabelValues(ev.Kind, out.Result).Inc()
		final, err := json.Marshal(out)
		if err != nil {
			return err
		}
		return tx.Q.UpdateIngestResult(ctx, dbq.UpdateIngestResultParams{BotInstanceID: bot.InstanceID, EventID: ev.EventID, Result: final, UserID: tx.UserID})
	})
	return out, err
}

func contentOf(parts []Part) grouping.Content {
	var c grouping.Content
	for _, p := range parts {
		if p.Type == PartText {
			c.HasText = true
		} else {
			c.HasMedia = true // attachments, failed attachments and unsupported content are not text bodies
		}
	}
	return c
}

// created handles a new message: it decides, through the grouping policy, whether the message
// starts a note or joins one, and stores its parts with their source references (docs/design/04 §2.1).
func (s *Service) created(ctx context.Context, tx *store.UserTx, bot *bots.Principal, ident bots.Identity, ev Event, now time.Time) (Outcome, error) {
	src := dbq.PartsBySourceMessageParams{UserID: tx.UserID, SourceBotInstanceID: uuid.NullUUID{UUID: bot.InstanceID, Valid: true},
		SourceConversationID: &ev.Conversation, SourceMessageID: &ev.MessageID}
	// A replay that outlived its ingest_events row (older than the retention): the unique source
	// reference proves the message is already stored, so answer as before instead of inserting again.
	// All events of one user are serialised by the user lock, so this check cannot race (BOT-7, BOT-B4).
	if existing, err := tx.Q.PartsBySourceMessage(ctx, src); err != nil {
		return Outcome{}, err
	} else if len(existing) > 0 {
		id := existing[0].NoteID
		return Outcome{Result: ResultCreated, NoteID: &id, Feedback: Feedback{React: "ok"}}, nil
	}

	user, err := tx.Q.GetUser(ctx, tx.UserID)
	if err != nil {
		return Outcome{}, err
	}
	decision := grouping.Decision{Target: grouping.New, Reason: grouping.ReasonFirst}
	if !ev.Standalone {
		if decision, err = s.decide(ctx, tx, ident, ev, s.window(user.Settings)); err != nil {
			return Outcome{}, err
		}
	}

	var noteID uuid.UUID
	var version int32
	result := ResultCreated
	if decision.Target == grouping.Existing {
		noteID, result = decision.NoteID, ResultAppended
	} else {
		noteID, err = uuid.NewV7()
		if err != nil {
			return Outcome{}, err
		}
		note, err := tx.Q.InsertNote(ctx, dbq.InsertNoteParams{ID: noteID, UserID: tx.UserID, CreatedAt: ev.Timestamp, ReceivedAt: now})
		if err != nil {
			return Outcome{}, err
		}
		version = note.Version
	}
	ordinal := int32(0)
	if decision.Target == grouping.Existing {
		if ordinal, err = tx.Q.NextPartOrdinal(ctx, noteID); err != nil {
			return Outcome{}, err
		}
	}
	var lost []lostFile
	for i, p := range ev.Parts {
		l, err := s.insertChatPart(ctx, tx, bot, ident, ev, i, p, noteID, ordinal+int32(i), decision)
		if err != nil {
			return Outcome{}, err
		}
		if l != nil {
			lost = append(lost, *l)
		}
	}
	if decision.Target == grouping.Existing {
		n, err := tx.Q.TouchNote(ctx, dbq.TouchNoteParams{ID: noteID, UserID: tx.UserID, UpdatedAt: now})
		if err != nil {
			return Outcome{}, err
		}
		version = n.Version
	}
	if err := tx.Change(ctx, "note", noteID, "upsert", &version); err != nil {
		return Outcome{}, err
	}
	obs.Grouping.WithLabelValues(decision.Reason).Inc()
	fb := Feedback{React: "ok"}
	if len(lost) > 0 {
		fb = failureFeedback(lost)
	}
	return Outcome{Result: result, NoteID: &noteID, Feedback: fb}, nil
}

// decide gathers the facts the pure grouping policy needs: the related note (reply or thread) and
// the sender's most recent active note in this conversation.
func (s *Service) decide(ctx context.Context, tx *store.UserTx, ident bots.Identity, ev Event, window time.Duration) (grouping.Decision, error) {
	var rel *grouping.Related
	for _, ref := range []struct{ id, reason string }{{ev.ReplyTo, grouping.ReasonReply}, {ev.Thread, grouping.ReasonThread}} {
		if ref.id == "" {
			continue
		}
		parts, err := tx.Q.PartsBySourceMessage(ctx, dbq.PartsBySourceMessageParams{UserID: tx.UserID, SourceBotInstanceID: uuid.NullUUID{UUID: ident.BotInstanceID, Valid: true},
			SourceConversationID: &ev.Conversation, SourceMessageID: &ref.id})
		if err != nil {
			return grouping.Decision{}, err
		}
		if len(parts) > 0 {
			rel = &grouping.Related{NoteID: parts[0].NoteID, PartID: parts[0].ID, Active: parts[0].NoteState == "active", Reason: ref.reason}
			break
		}
	}
	var cand *grouping.Candidate
	if rel == nil {
		recent, err := tx.Q.SenderRecentNote(ctx, dbq.SenderRecentNoteParams{UserID: tx.UserID, SourceIdentityID: uuid.NullUUID{UUID: ident.ID, Valid: true}, SourceConversationID: &ev.Conversation})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return grouping.Decision{}, err
		}
		if err == nil {
			hasText, err := tx.Q.NoteHasText(ctx, recent.NoteID)
			if err != nil {
				return grouping.Decision{}, err
			}
			cand = &grouping.Candidate{NoteID: recent.NoteID, LastPartAt: recent.LastPartAt, HasText: hasText}
		}
	}
	return grouping.Decide(grouping.Event{Timestamp: ev.Timestamp, Content: contentOf(ev.Parts)}, cand, rel, window), nil
}

// insertChatPart stores one part of a chat message with its source reference and history entry.
func (s *Service) insertChatPart(ctx context.Context, tx *store.UserTx, bot *bots.Principal, ident bots.Identity, ev Event, index int, p Part,
	noteID uuid.UUID, ordinal int32, d grouping.Decision) (*lostFile, error) {
	partID, _ := uuid.NewV7()
	params := dbq.InsertNotePartParams{
		ID: partID, UserID: tx.UserID, NoteID: noteID, Ordinal: ordinal, AttachReason: d.Reason, CreatedAt: ev.Timestamp,
		SourceBotInstanceID:  uuid.NullUUID{UUID: bot.InstanceID, Valid: true},
		SourceIdentityID:     uuid.NullUUID{UUID: ident.ID, Valid: true},
		SourceConversationID: &ev.Conversation, SourceMessageID: &ev.MessageID, SourcePartIndex: pgInt2(index),
	}
	if d.RelatedPartID != nil && index == 0 {
		params.RelatedPartID = uuid.NullUUID{UUID: *d.RelatedPartID, Valid: true}
	}
	var lost *lostFile
	failed := func(name, reason string, size int64) {
		lost = &lostFile{name: name, reason: reason}
		params.Kind, params.FailedFilename, params.FailedReason = "failed_attachment", &name, &reason
		if size > 0 {
			params.FailedSize = &size
		}
	}
	switch p.Type {
	case PartText:
		params.Kind, params.Text = "text", &p.Text
	case PartUnsupported:
		desc := p.Description
		if desc == "" {
			desc = "Unsupported content"
		}
		params.Kind, params.Text = "unsupported", &desc
	case PartAttachmentFailed:
		failed(p.Filename, orDefault(p.Reason, "failed"), p.Size)
	default: // PartAttachment: the bot uploaded the file first; CORE-A9 says a lost upload must not lose the message
		linked, err := s.linkUpload(ctx, tx, p)
		if err != nil {
			return nil, err
		}
		if linked == uuid.Nil {
			failed(p.Filename, "upload_missing", p.Size)
		} else {
			params.Kind, params.AttachmentID = "attachment", uuid.NullUUID{UUID: linked, Valid: true}
		}
	}
	if _, err := tx.Q.InsertNotePart(ctx, params); err != nil {
		return nil, fmt.Errorf("insert part: %w", err)
	}
	if params.Text != nil && p.Type == PartText { // the first history entry of a text part
		vid, _ := uuid.NewV7()
		evID := ev.EventID
		if err := tx.Q.InsertPartVersion(ctx, dbq.InsertPartVersionParams{ID: vid, UserID: tx.UserID, PartID: partID, Text: *params.Text,
			Origin: "chat", EditedAt: ev.Timestamp, Applied: true, SourceEventID: &evID}); err != nil {
			return nil, fmt.Errorf("insert version: %w", err)
		}
	}
	return lost, nil
}

// lostFile is an attachment that could not be kept.
type lostFile struct{ name, reason string }

var reasonWords = map[string]string{
	"too_large": "it is too large", "quota_exceeded": "your storage is full", "upload_missing": "the upload was lost",
	"download_failed": "I could not download it from the chat", "corrupt": "it arrived damaged",
	"size_unknown": "its size could not be determined", "unsupported_encryption": "its encryption is not supported",
	"upload_failed": "it could not be stored",
}

// failureFeedback tells the person which files were not kept and why (CORE-A9, MX-7). The text of
// the message is safe either way, and the reply says so.
func failureFeedback(lost []lostFile) Feedback {
	var b strings.Builder
	b.WriteString("Saved, but I could not keep ")
	if len(lost) == 1 {
		b.WriteString("the file")
	} else {
		b.WriteString("these files")
	}
	for i, l := range lost {
		if i > 0 {
			b.WriteString(";")
		}
		why, ok := reasonWords[l.reason]
		if !ok {
			why = "it could not be stored"
		}
		name := l.name
		if name == "" {
			name = "file"
		}
		fmt.Fprintf(&b, " %q: %s", name, why)
	}
	b.WriteString(".")
	text := b.String()
	return Feedback{React: "⚠️", ReplyText: &text}
}

// linkUpload resolves the upload an attachment part refers to. It returns uuid.Nil when the upload
// no longer exists (the janitor removed it during a long Core outage) or is already used, so the
// caller records a failed attachment and the message text still lands (CORE-A9).
func (s *Service) linkUpload(ctx context.Context, tx *store.UserTx, p Part) (uuid.UUID, error) {
	if p.UploadID == nil {
		return uuid.Nil, nil
	}
	if _, err := tx.Q.GetAttachment(ctx, dbq.GetAttachmentParams{ID: *p.UploadID, UserID: tx.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, nil
		}
		return uuid.Nil, err
	}
	used, err := tx.Q.AttachmentInUse(ctx, dbq.AttachmentInUseParams{AttachmentID: uuid.NullUUID{UUID: *p.UploadID, Valid: true}, UserID: tx.UserID})
	if err != nil || used {
		return uuid.Nil, err
	}
	return *p.UploadID, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// edited applies a chat edit to the parts that message created. The latest edit wins by the time
// it was MADE, not by arrival: a delayed chat edit that lost to a later app edit is kept in the
// history without overwriting (EDT-2, EDT-5, docs/design/04 §2.2).
func (s *Service) edited(ctx context.Context, tx *store.UserTx, bot *bots.Principal, ident bots.Identity, ev Event, now time.Time) (Outcome, error) {
	parts, err := tx.Q.PartsBySourceMessage(ctx, dbq.PartsBySourceMessageParams{UserID: tx.UserID, SourceBotInstanceID: uuid.NullUUID{UUID: bot.InstanceID, Valid: true},
		SourceConversationID: &ev.Conversation, SourceMessageID: &ev.MessageID})
	if err != nil {
		return Outcome{}, err
	}
	// The text parts this identity wrote in that message, in message order. A message has one text
	// (its body, or the caption of a file), so the k-th new text replaces the k-th old text.
	var olds []dbq.PartsBySourceMessageRow
	for _, old := range parts {
		if old.Kind == "text" && old.SourceIdentityID.Valid && old.SourceIdentityID.UUID == ident.ID && old.SourcePartIndex.Valid { // SEC-BOT-1
			olds = append(olds, old)
		}
	}
	slices.SortFunc(olds, func(a, b dbq.PartsBySourceMessageRow) int {
		return int(a.SourcePartIndex.Int16) - int(b.SourcePartIndex.Int16)
	})
	changed := map[uuid.UUID]bool{}
	seen := false
	k := 0
	for _, np := range ev.Parts {
		if np.Type != PartText {
			continue
		}
		if k >= len(olds) {
			break
		}
		old := olds[k]
		k++
		seen = true
		applied := old.TextEditedAt == nil || ev.Timestamp.After(*old.TextEditedAt)
		vid, _ := uuid.NewV7()
		evID := ev.EventID
		if err := tx.Q.InsertPartVersion(ctx, dbq.InsertPartVersionParams{ID: vid, UserID: tx.UserID, PartID: old.ID, Text: np.Text,
			Origin: "chat", EditedAt: ev.Timestamp, Applied: applied, SourceEventID: &evID}); err != nil {
			return Outcome{}, err
		}
		if applied && (old.Text == nil || *old.Text != np.Text) {
			at := ev.Timestamp
			if err := tx.Q.UpdatePartText(ctx, dbq.UpdatePartTextParams{ID: old.ID, Text: &np.Text, TextEditedAt: &at}); err != nil {
				return Outcome{}, err
			}
			changed[old.NoteID] = true
		}
	}
	if !seen {
		return Outcome{Result: ResultIgnored}, nil // EDT-6: a message Core does not know is ignored without error
	}
	var noteID *uuid.UUID
	for id := range changed { // the note stays wherever it is, in the Trash included (EDT-7)
		n, err := tx.Q.TouchNote(ctx, dbq.TouchNoteParams{ID: id, UserID: tx.UserID, UpdatedAt: now})
		if err != nil {
			return Outcome{}, err
		}
		if err := tx.Change(ctx, "note", id, "upsert", &n.Version); err != nil {
			return Outcome{}, err
		}
		nid := id
		noteID = &nid
	}
	if noteID == nil {
		nid := parts[0].NoteID
		noteID = &nid
	}
	return Outcome{Result: ResultUpdated, NoteID: noteID, Feedback: Feedback{React: "ok"}}, nil
}

// deleted removes the parts a deleted chat message created. If they were all the parts of the
// note, the note goes to the Trash with its content intact instead: the Trash is the undo, and
// nothing a person wrote is destroyed by someone deleting a chat message (EDT-4, decision 51).
func (s *Service) deleted(ctx context.Context, tx *store.UserTx, bot *bots.Principal, ident bots.Identity, ev Event, now time.Time) (Outcome, error) {
	parts, err := tx.Q.PartsBySourceMessage(ctx, dbq.PartsBySourceMessageParams{UserID: tx.UserID, SourceBotInstanceID: uuid.NullUUID{UUID: bot.InstanceID, Valid: true},
		SourceConversationID: &ev.Conversation, SourceMessageID: &ev.MessageID})
	if err != nil {
		return Outcome{}, err
	}
	var mine []dbq.PartsBySourceMessageRow
	for _, p := range parts {
		if p.SourceIdentityID.Valid && p.SourceIdentityID.UUID == ident.ID {
			mine = append(mine, p)
		}
	}
	if len(mine) == 0 {
		return Outcome{Result: ResultIgnored}, nil
	}
	noteID := mine[0].NoteID
	total, err := tx.Q.CountNoteParts(ctx, noteID)
	if err != nil {
		return Outcome{}, err
	}
	var version int32
	if int(total) <= len(mine) {
		if mine[0].NoteState == "active" {
			n, err := tx.Q.DismissNote(ctx, dbq.DismissNoteParams{ID: noteID, UserID: tx.UserID, DeletedAt: &now})
			if err != nil {
				return Outcome{}, err
			}
			version = n.Version
			if err := reminders.OnDismiss(ctx, tx, noteID); err != nil {
				return Outcome{}, err
			}
		} else {
			return Outcome{Result: ResultRemoved, NoteID: &noteID}, nil
		}
	} else {
		for _, p := range mine {
			if err := tx.Q.DeleteNotePart(ctx, p.ID); err != nil {
				return Outcome{}, err
			}
			if p.AttachmentID.Valid {
				if err := s.Blobs.Delete(ctx, tx, p.AttachmentID.UUID); err != nil {
					return Outcome{}, err
				}
			}
		}
		n, err := tx.Q.TouchNote(ctx, dbq.TouchNoteParams{ID: noteID, UserID: tx.UserID, UpdatedAt: now})
		if err != nil {
			return Outcome{}, err
		}
		version = n.Version
	}
	if err := tx.Change(ctx, "note", noteID, "upsert", &version); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: ResultRemoved, NoteID: &noteID}, nil
}

func pgInt2(i int) pgtype.Int2 { return pgtype.Int2{Int16: int16(i), Valid: true} }
