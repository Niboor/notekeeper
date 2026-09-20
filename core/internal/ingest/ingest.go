// Package ingest turns normalised chat events into notes (docs/design/04-ingestion.md). Bots
// forward events; everything they mean is decided here, in one transaction per event that
// first locks the user's row, so concurrent deliveries can never both decide "new note".
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Niboor/notekeeper/core/internal/bots"
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
	St   *store.Store
	Bots *bots.Service
	Now  func() time.Time
}

// New creates the service.
func New(st *store.Store, b *bots.Service) *Service {
	return &Service{St: st, Bots: b, Now: time.Now}
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
			raw, err := tx.Q.GetIngestEvent(ctx, dbq.GetIngestEventParams{BotInstanceID: bot.InstanceID, EventID: ev.EventID})
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
			default:
				out = Outcome{Result: ResultIgnored} // edits and deletions arrive with milestone M3
			}
			if err != nil {
				return err
			}
		}
		final, err := json.Marshal(out)
		if err != nil {
			return err
		}
		return tx.Q.UpdateIngestResult(ctx, dbq.UpdateIngestResultParams{BotInstanceID: bot.InstanceID, EventID: ev.EventID, Result: final})
	})
	return out, err
}

func (s *Service) created(ctx context.Context, tx *store.UserTx, bot *bots.Principal, ident bots.Identity, ev Event, now time.Time) (Outcome, error) {
	noteID, err := uuid.NewV7()
	if err != nil {
		return Outcome{}, err
	}
	note, err := tx.Q.InsertNote(ctx, dbq.InsertNoteParams{ID: noteID, UserID: tx.UserID, CreatedAt: ev.Timestamp, ReceivedAt: now})
	if err != nil {
		return Outcome{}, err
	}
	for i, p := range ev.Parts {
		partID, _ := uuid.NewV7()
		params := dbq.InsertNotePartParams{
			ID: partID, UserID: tx.UserID, NoteID: noteID, Ordinal: int32(i), AttachReason: "first", CreatedAt: ev.Timestamp,
			SourceBotInstanceID:  uuid.NullUUID{UUID: bot.InstanceID, Valid: true},
			SourceIdentityID:     uuid.NullUUID{UUID: ident.ID, Valid: true},
			SourceConversationID: &ev.Conversation, SourceMessageID: &ev.MessageID,
			SourcePartIndex: pgInt2(i),
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
		default:
			// Attachments arrive with milestone M2 (storage) and M3 (chat side); until then they are
			// recorded as failed so that no message is ever dropped silently (CORE-A9).
			name, reason := p.Filename, "not_supported_yet"
			params.Kind, params.FailedFilename, params.FailedReason = "failed_attachment", &name, &reason
			if p.Size > 0 {
				params.FailedSize = &p.Size
			}
		}
		if _, err := tx.Q.InsertNotePart(ctx, params); err != nil {
			return Outcome{}, fmt.Errorf("insert part: %w", err)
		}
		if params.Text != nil { // the first history entry of a text part
			vid, _ := uuid.NewV7()
			evID := ev.EventID
			if err := tx.Q.InsertPartVersion(ctx, dbq.InsertPartVersionParams{ID: vid, UserID: tx.UserID, PartID: partID, Text: *params.Text,
				Origin: "chat", EditedAt: ev.Timestamp, Applied: true, SourceEventID: &evID}); err != nil {
				return Outcome{}, fmt.Errorf("insert version: %w", err)
			}
		}
	}
	if err := tx.Change(ctx, "note", noteID, "upsert", &note.Version); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: ResultCreated, NoteID: &noteID, Feedback: Feedback{React: "ok"}}, nil
}

func pgInt2(i int) pgtype.Int2 { return pgtype.Int2{Int16: int16(i), Valid: true} }
