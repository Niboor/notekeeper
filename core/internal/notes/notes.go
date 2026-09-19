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
type Service struct{ St *store.Store }

// New creates the service.
func New(st *store.Store) *Service { return &Service{St: st} }

// Part is a note part with the type of the bot it came from, if any (for "via Matrix").
type Part struct {
	dbq.NotePart
	SourceBotType *string
}

// Note is a note with its parts.
type Note struct {
	Note  dbq.Note
	Parts []Part
}

// Page is one page of a listing.
type Page struct {
	Items      []Note
	NextCursor string
	// Total is the number of notes in the whole listing, when cheap to know.
	Total *int64
}

func encodeCursor(t time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(c string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	ts, idStr, ok := strings.Cut(string(raw), "|")
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
	byNote := make(map[uuid.UUID][]Part, len(notes))
	for _, p := range parts {
		byNote[p.NotePart.NoteID] = append(byNote[p.NotePart.NoteID], Part{NotePart: p.NotePart, SourceBotType: p.SourceBotType})
	}
	out := make([]Note, len(notes))
	for i, n := range notes {
		out[i] = Note{Note: n, Parts: byNote[n.ID]}
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
