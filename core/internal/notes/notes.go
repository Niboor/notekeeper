// Package notes holds the read and write operations on notes, pages and categories that the
// user API exposes (docs/design/02-api.md section 1.2).
package notes

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/blobs"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// ErrInvalidCursor is returned for a pagination cursor that was not issued by the server.
var ErrInvalidCursor = errors.New("invalid cursor")

// Limits for paging (design README section 3): default 50, maximum 200.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Service is the notes service.
type Service struct {
	St    *store.Store
	Blobs *blobs.Service
	Now   func() time.Time
}

// New creates the service.
func New(st *store.Store, b *blobs.Service) *Service { return &Service{St: st, Blobs: b} }

// Part is a note part with the type of the bot it came from, if any (for "via Matrix"), and,
// for attachment parts, what the attachment is.
type Part struct {
	dbq.NotePart
	SourceBotType *string
	Attachment    *AttachmentInfo
}

// AttachmentInfo describes an attachment for display.
type AttachmentInfo struct {
	ID        uuid.UUID
	Filename  string
	MediaType string
	Size      int64
}

// Location says where a dismissed note came from (the Trash view).
type Location struct {
	CategoryID   uuid.UUID
	CategoryName string
	PageID       uuid.UUID
	PageName     string
}

// Note is a note with its parts.
type Note struct {
	Note  dbq.Note
	Parts []Part
	// Reminders are the note's reminders, soonest first (CORE-R1, CORE-R8).
	Reminders []dbq.Reminder
	// Location is set for dismissed notes that were in a category.
	Location *Location
}

// Page is one page of a listing.
type Page struct {
	Items      []Note
	NextCursor string
	// Total is the number of notes in the whole listing, when cheap to know.
	Total *int64
}

func encodeCursorRaw(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func decodeCursorRaw(c string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", ErrInvalidCursor
	}
	return string(raw), nil
}

func encodeCursor(t time.Time, id uuid.UUID) string {
	return encodeCursorRaw(t.UTC().Format(time.RFC3339Nano) + "|" + id.String())
}

func decodeCursor(c string) (time.Time, uuid.UUID, error) {
	raw, err := decodeCursorRaw(c)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	ts, idStr, ok := strings.Cut(raw, "|")
	if !ok {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	return t, id, nil
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	}
	return n
}

func withParts(ctx context.Context, q *dbq.Queries, notes []dbq.Note) ([]Note, error) {
	ids := make([]uuid.UUID, len(notes))
	for i, n := range notes {
		ids[i] = n.ID
	}
	parts, err := q.ListNoteParts(ctx, ids)
	if err != nil {
		return nil, err
	}
	var attIDs []uuid.UUID
	for _, p := range parts {
		if p.NotePart.AttachmentID.Valid {
			attIDs = append(attIDs, p.NotePart.AttachmentID.UUID)
		}
	}
	atts := map[uuid.UUID]AttachmentInfo{}
	if len(attIDs) > 0 {
		rows, err := q.PartAttachmentInfo(ctx, dbq.PartAttachmentInfoParams{Column1: attIDs, UserID: notes[0].UserID})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			atts[r.ID] = AttachmentInfo{ID: r.ID, Filename: r.Filename, MediaType: r.MediaType, Size: r.SizeBytes}
		}
	}
	rems, err := q.ListRemindersForNotes(ctx, ids)
	if err != nil {
		return nil, err
	}
	remsByNote := make(map[uuid.UUID][]dbq.Reminder, len(notes))
	for _, r := range rems {
		remsByNote[r.NoteID] = append(remsByNote[r.NoteID], r)
	}
	byNote := make(map[uuid.UUID][]Part, len(notes))
	for _, p := range parts {
		part := Part{NotePart: p.NotePart, SourceBotType: p.SourceBotType}
		if p.NotePart.AttachmentID.Valid {
			if a, ok := atts[p.NotePart.AttachmentID.UUID]; ok {
				part.Attachment = &a
			}
		}
		byNote[p.NotePart.NoteID] = append(byNote[p.NotePart.NoteID], part)
	}
	out := make([]Note, len(notes))
	for i, n := range notes {
		out[i] = Note{Note: n, Parts: byNote[n.ID], Reminders: remsByNote[n.ID]}
	}
	return out, nil
}

// ListInbox returns the Inbox, newest first (design decision D1), one page at a time.
func (s *Service) ListInbox(ctx context.Context, user uuid.UUID, limit int, cursor string) (Page, error) {
	limit = clampLimit(limit)
	var (
		after   *time.Time
		afterID uuid.UUID
	)
	if cursor != "" {
		t, id, err := decodeCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		after, afterID = &t, id
	}
	var page Page
	err := s.St.InUserRead(ctx, user, func(q *dbq.Queries) error {
		rows, err := q.ListInbox(ctx, dbq.ListInboxParams{UserID: user, AfterCreated: after, AfterID: uuid.NullUUID{UUID: afterID, Valid: after != nil}, Limit: int32(limit + 1)})
		if err != nil {
			return err
		}
		more := len(rows) > limit
		if more {
			rows = rows[:limit]
		}
		if page.Items, err = withParts(ctx, q, rows); err != nil {
			return err
		}
		if more {
			last := rows[len(rows)-1]
			page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
		}
		total, err := q.CountInbox(ctx, user)
		if err != nil {
			return err
		}
		page.Total = &total
		return nil
	})
	return page, err
}

// Get returns one note. A note of another user is indistinguishable from a missing one (SEC-ISO-3).
func (s *Service) Get(ctx context.Context, user, id uuid.UUID) (Note, error) {
	var out Note
	err := s.St.InUserRead(ctx, user, func(q *dbq.Queries) error {
		n, err := q.GetNote(ctx, dbq.GetNoteParams{ID: id, UserID: user})
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		res, err := withParts(ctx, q, []dbq.Note{n})
		if err != nil {
			return err
		}
		out = res[0]
		return nil
	})
	return out, err
}
