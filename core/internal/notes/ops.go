package notes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/fracindex"
	"github.com/Niboor/notekeeper/core/internal/position"
	"github.com/Niboor/notekeeper/core/internal/reminders"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// Errors of the note operations.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrConflict     = errors.New("conflict")
)

// Limits (SEC-API-3).
const (
	MaxTextLength   = 100_000
	MaxPartsPerNote = 50
	// coalesceWindow merges rapid app edits of one part by one session into a single history entry
	// (docs/design/01-data-model.md section 4.2, CORE-N17).
	coalesceWindow = 60 * time.Second
)

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidInput, msg) }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

// ---- reading ---------------------------------------------------------------------------------

// CategoryPage returns one page of a category's notes in their manual order. It runs inside the
// caller's read transaction.
func (s *Service) CategoryPage(ctx context.Context, q *dbq.Queries, user, category uuid.UUID, limit int, cursor string) (Page, error) {
	limit = clampLimit(limit)
	var afterPos *string
	var afterID uuid.NullUUID
	if cursor != "" {
		pos, id, err := decodeKeyCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		afterPos, afterID = &pos, uuid.NullUUID{UUID: id, Valid: true}
	}
	rows, err := q.ListCategoryNotes(ctx, dbq.ListCategoryNotesParams{UserID: user, CategoryID: uuid.NullUUID{UUID: category, Valid: true},
		AfterPosition: afterPos, AfterID: afterID, Limit: int32(limit + 1)})
	if err != nil {
		return Page{}, err
	}
	var page Page
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	if page.Items, err = withParts(ctx, q, rows); err != nil {
		return Page{}, err
	}
	if more {
		last := rows[len(rows)-1]
		page.NextCursor = encodeKeyCursor(*last.Position, last.ID)
	}
	return page, nil
}

// ListCategoryNotes lists a category's notes for the API. An unknown or foreign category is "not found".
func (s *Service) ListCategoryNotes(ctx context.Context, user, category uuid.UUID, limit int, cursor string) (Page, error) {
	var page Page
	err := s.St.InUserRead(ctx, user, func(q *dbq.Queries) error {
		if _, err := q.GetCategory(ctx, dbq.GetCategoryParams{ID: category, UserID: user}); err != nil {
			return notFound(err)
		}
		var err error
		page, err = s.CategoryPage(ctx, q, user, category, limit, cursor)
		if err != nil {
			return err
		}
		total, err := q.CountCategoryNotes(ctx, dbq.CountCategoryNotesParams{UserID: user, CategoryID: uuid.NullUUID{UUID: category, Valid: true}})
		page.Total = &total
		return err
	})
	return page, err
}

// ListTrash lists dismissed notes, newest dismissal first, each with where it came from (CORE-N9).
func (s *Service) ListTrash(ctx context.Context, user uuid.UUID, limit int, cursor string) (Page, error) {
	limit = clampLimit(limit)
	var after *time.Time
	var afterID uuid.NullUUID
	if cursor != "" {
		t, id, err := decodeCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		after, afterID = &t, uuid.NullUUID{UUID: id, Valid: true}
	}
	var page Page
	err := s.St.InUserRead(ctx, user, func(q *dbq.Queries) error {
		rows, err := q.ListTrash(ctx, dbq.ListTrashParams{UserID: user, AfterDeleted: after, AfterID: afterID, Limit: int32(limit + 1)})
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
		if err := attachLocations(ctx, q, user, page.Items); err != nil {
			return err
		}
		if more {
			last := rows[len(rows)-1]
			page.NextCursor = encodeCursor(*last.DeletedAt, last.ID)
		}
		total, err := q.CountTrash(ctx, user)
		page.Total = &total
		return err
	})
	return page, err
}

func attachLocations(ctx context.Context, q *dbq.Queries, user uuid.UUID, items []Note) error {
	var ids []uuid.UUID
	for _, n := range items {
		if n.Note.CategoryID.Valid {
			ids = append(ids, n.Note.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	locs, err := q.NoteLocations(ctx, dbq.NoteLocationsParams{UserID: user, Column2: ids})
	if err != nil {
		return err
	}
	byNote := map[uuid.UUID]dbq.NoteLocationsRow{}
	for _, l := range locs {
		byNote[l.NoteID] = l
	}
	for i := range items {
		if l, ok := byNote[items[i].Note.ID]; ok {
			items[i].Location = &Location{CategoryID: l.CategoryID, CategoryName: l.CategoryName, PageID: l.PageID, PageName: l.PageName}
		}
	}
	return nil
}

// ---- creating ----------------------------------------------------------------------------------

// PartInput is one part of a note being created or extended in the app.
type PartInput struct {
	Text         *string
	AttachmentID *uuid.UUID
}

// CreateInput describes a note created in the app (WEB-9).
type CreateInput struct {
	ID                *uuid.UUID
	CategoryID        *uuid.UUID
	BeforeID, AfterID *uuid.UUID
	Parts             []PartInput
}

func validateText(t string) (string, error) {
	if strings.TrimSpace(t) == "" {
		return "", invalid("text must not be empty")
	}
	if utf8.RuneCountInString(t) > MaxTextLength {
		return "", invalid("text too long")
	}
	if !utf8.ValidString(t) || strings.ContainsRune(t, 0) {
		return "", invalid("text contains invalid characters")
	}
	return strings.TrimRight(t, " \t\r\n"), nil
}

// Create creates a note in the Inbox or at the top of a category (or between given neighbours).
// The client may supply the id; repeating it with the same content returns the note (CORE-S5).
func (s *Service) Create(ctx context.Context, user uuid.UUID, in CreateInput) (Note, error) {
	if len(in.Parts) == 0 || len(in.Parts) > 20 {
		return Note{}, invalid("a note needs 1 to 20 parts")
	}
	texts := make([]string, len(in.Parts))
	for i, p := range in.Parts {
		switch {
		case p.Text != nil && p.AttachmentID == nil:
			t, err := validateText(*p.Text)
			if err != nil {
				return Note{}, err
			}
			texts[i] = t
		case p.AttachmentID != nil && p.Text == nil:
			// attachments are linked below
		default:
			return Note{}, invalid("each part is either text or an attachment")
		}
	}
	now := s.now()
	var out Note
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if in.ID != nil {
			id = *in.ID
			if existing, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: id, UserID: user}); err == nil {
				same, err := s.sameParts(ctx, tx.Q, existing.ID, in.Parts, texts)
				if err != nil {
					return err
				}
				if !same {
					return ErrConflict
				}
				res, err := withParts(ctx, tx.Q, []dbq.Note{existing})
				out = res[0]
				return err
			}
		}
		var category uuid.NullUUID
		var key *string
		if in.CategoryID != nil {
			if _, err := tx.Q.GetCategory(ctx, dbq.GetCategoryParams{ID: *in.CategoryID, UserID: user}); err != nil {
				return notFound(err) // a foreign category looks like a missing one (SEC-ISO-4)
			}
			k, err := s.NoteGroup(tx, *in.CategoryID, id, now).Place(ctx, position.Hints{AfterID: in.AfterID, BeforeID: in.BeforeID, DefaultTop: true})
			if err != nil {
				return mapPosition(err)
			}
			category, key = uuid.NullUUID{UUID: *in.CategoryID, Valid: true}, &k
		}
		note, err := tx.Q.InsertNoteAt(ctx, dbq.InsertNoteAtParams{ID: id, UserID: user, CategoryID: category, Position: key, CreatedAt: now})
		if err != nil {
			if store.IsUniqueViolation(err, "") {
				return ErrConflict // an id owned by someone else looks like a conflicting repeat
			}
			return err
		}
		for i, p := range in.Parts {
			if err := s.insertPart(ctx, tx, note.ID, int32(i), p, texts[i], now); err != nil {
				return err
			}
		}
		if err := tx.Change(ctx, "note", note.ID, "upsert", &note.Version); err != nil {
			return err
		}
		res, err := withParts(ctx, tx.Q, []dbq.Note{note})
		out = res[0]
		return err
	})
	return out, err
}

func (s *Service) sameParts(ctx context.Context, q *dbq.Queries, note uuid.UUID, in []PartInput, texts []string) (bool, error) {
	parts, err := q.ListNoteParts(ctx, []uuid.UUID{note})
	if err != nil || len(parts) != len(in) {
		return false, err
	}
	for i, p := range parts {
		if in[i].AttachmentID != nil {
			if !p.NotePart.AttachmentID.Valid || p.NotePart.AttachmentID.UUID != *in[i].AttachmentID {
				return false, nil
			}
			continue
		}
		if p.NotePart.Text == nil || *p.NotePart.Text != texts[i] {
			return false, nil
		}
	}
	return true, nil
}

// insertPart appends a part written in the app, with its first history entry.
func (s *Service) insertPart(ctx context.Context, tx *store.UserTx, note uuid.UUID, ordinal int32, in PartInput, text string, now time.Time) error {
	partID, _ := uuid.NewV7()
	params := dbq.InsertNotePartParams{ID: partID, UserID: tx.UserID, NoteID: note, Ordinal: ordinal, AttachReason: "app", CreatedAt: now}
	if in.AttachmentID != nil {
		// An attachment uploaded earlier by this user and not yet used by any part.
		if _, err := tx.Q.GetAttachment(ctx, dbq.GetAttachmentParams{ID: *in.AttachmentID, UserID: tx.UserID}); err != nil {
			return notFound(err) // a foreign attachment looks like a missing one (SEC-ISO-4)
		}
		if used, err := tx.Q.AttachmentInUse(ctx, dbq.AttachmentInUseParams{AttachmentID: uuid.NullUUID{UUID: *in.AttachmentID, Valid: true}, UserID: tx.UserID}); err != nil {
			return err
		} else if used {
			return ErrConflict
		}
		params.Kind, params.AttachmentID = "attachment", uuid.NullUUID{UUID: *in.AttachmentID, Valid: true}
		_, err := tx.Q.InsertNotePart(ctx, params)
		return err
	}
	params.Kind, params.Text = "text", &text
	if _, err := tx.Q.InsertNotePart(ctx, params); err != nil {
		return err
	}
	vid, _ := uuid.NewV7()
	return tx.Q.InsertPartVersion(ctx, dbq.InsertPartVersionParams{ID: vid, UserID: tx.UserID, PartID: partID, Text: text,
		Origin: "app", EditedAt: now, Applied: true})
}

// NoteGroup describes the manual order of a category's active notes for placement. The note
// being placed (exclude) is not part of the answers.
func (s *Service) NoteGroup(tx *store.UserTx, category, exclude uuid.UUID, now time.Time) position.Group {
	u, cat := tx.UserID, uuid.NullUUID{UUID: category, Valid: true}
	opt := func(key *string, err error) (string, bool, error) {
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && key == nil) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		return *key, true, nil
	}
	return position.Group{
		KeyOf: func(ctx context.Context, id uuid.UUID) (string, bool, error) {
			if id == exclude {
				return "", false, nil
			}
			k, err := tx.Q.NotePosition(ctx, dbq.NotePositionParams{UserID: u, ID: id, CategoryID: cat})
			return opt(k, err)
		},
		After: func(ctx context.Context, key string) (string, bool, error) {
			k, err := tx.Q.NoteKeyAfter(ctx, dbq.NoteKeyAfterParams{UserID: u, CategoryID: cat, ID: exclude, Position: &key})
			return opt(k, err)
		},
		Before: func(ctx context.Context, key string) (string, bool, error) {
			k, err := tx.Q.NoteKeyBefore(ctx, dbq.NoteKeyBeforeParams{UserID: u, CategoryID: cat, ID: exclude, Position: &key})
			return opt(k, err)
		},
		First: func(ctx context.Context) (string, bool, error) {
			k, err := tx.Q.NoteKeyFirst(ctx, dbq.NoteKeyFirstParams{UserID: u, CategoryID: cat, ID: exclude})
			return opt(k, err)
		},
		Last: func(ctx context.Context) (string, bool, error) {
			k, err := tx.Q.NoteKeyLast(ctx, dbq.NoteKeyLastParams{UserID: u, CategoryID: cat, ID: exclude})
			return opt(k, err)
		},
		Rebalance: func(ctx context.Context) error {
			ids, err := tx.Q.CategoryNotePositions(ctx, dbq.CategoryNotePositionsParams{UserID: u, CategoryID: cat, ID: exclude})
			if err != nil {
				return err
			}
			for i, k := range fracindex.Even(len(ids)) {
				v, err := tx.Q.RekeyNote(ctx, dbq.RekeyNoteParams{ID: ids[i], UserID: u, Position: &k, UpdatedAt: now})
				if err != nil {
					return err
				}
				if err := tx.Change(ctx, "note", ids[i], "upsert", &v); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func mapPosition(err error) error {
	if errors.Is(err, position.ErrNeighbour) {
		return store.ErrNotFound
	}
	return err
}

// ---- moving, dismissing, restoring, deleting ------------------------------------------------------

// MoveInput says where a note goes.
type MoveInput struct {
	CategoryID        *uuid.UUID // nil = Inbox
	BeforeID, AfterID *uuid.UUID
}

// Move places a note in a category at the given position, or returns it to the Inbox (CORE-N3).
// Only the moved note's key is written, so moves of different notes never conflict (CORE-S4).
func (s *Service) Move(ctx context.Context, user, id uuid.UUID, in MoveInput) (Note, error) {
	now := s.now()
	var out Note
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		cur, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: id, UserID: user})
		if err != nil {
			return notFound(err)
		}
		if cur.State != "active" {
			return ErrConflict // restore it first
		}
		var category uuid.NullUUID
		var key *string
		if in.CategoryID != nil {
			if _, err := tx.Q.GetCategory(ctx, dbq.GetCategoryParams{ID: *in.CategoryID, UserID: user}); err != nil {
				return notFound(err)
			}
			k, err := s.NoteGroup(tx, *in.CategoryID, id, now).Place(ctx, position.Hints{AfterID: in.AfterID, BeforeID: in.BeforeID, DefaultTop: true})
			if err != nil {
				return mapPosition(err)
			}
			category, key = uuid.NullUUID{UUID: *in.CategoryID, Valid: true}, &k
		}
		n, err := tx.Q.SetNoteLocation(ctx, dbq.SetNoteLocationParams{ID: id, UserID: user, CategoryID: category, Position: key, UpdatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.Change(ctx, "note", id, "upsert", &n.Version); err != nil {
			return err
		}
		res, err := withParts(ctx, tx.Q, []dbq.Note{n})
		out = res[0]
		return err
	})
	return out, err
}

// Dismiss soft-deletes a note; it keeps its category and position so Restore puts it back (CORE-N6).
func (s *Service) Dismiss(ctx context.Context, user, id uuid.UUID) (Note, error) {
	return s.transition(ctx, user, id, func(tx *store.UserTx, now time.Time) (dbq.Note, error) {
		return tx.Q.DismissNote(ctx, dbq.DismissNoteParams{ID: id, UserID: user, DeletedAt: &now})
	})
}

// Restore undoes a dismissal. If the note's category no longer exists it lands in the Inbox (CORE-N7).
func (s *Service) Restore(ctx context.Context, user, id uuid.UUID) (Note, error) {
	return s.transition(ctx, user, id, func(tx *store.UserTx, now time.Time) (dbq.Note, error) {
		return tx.Q.RestoreNote(ctx, dbq.RestoreNoteParams{ID: id, UserID: user, UpdatedAt: now})
	})
}

func (s *Service) transition(ctx context.Context, user, id uuid.UUID, do func(*store.UserTx, time.Time) (dbq.Note, error)) (Note, error) {
	var out Note
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		if _, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: id, UserID: user}); err != nil {
			return notFound(err)
		}
		n, err := do(tx, s.now())
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict // already in that state: the second of two identical requests
		}
		if err != nil {
			return err
		}
		// Dismissing suspends the note's reminders; restoring re-arms the ones still ahead (CORE-R7).
		if n.State == "deleted" {
			err = reminders.OnDismiss(ctx, tx, id)
		} else {
			err = reminders.OnRestore(ctx, tx, id, s.now())
		}
		if err != nil {
			return err
		}
		if err := tx.Change(ctx, "note", id, "upsert", &n.Version); err != nil {
			return err
		}
		res, err := withParts(ctx, tx.Q, []dbq.Note{n})
		if err != nil {
			return err
		}
		if n.State == "deleted" {
			if err := attachLocations(ctx, tx.Q, user, res); err != nil {
				return err
			}
		}
		out = res[0]
		return nil
	})
	return out, err
}

// DeletePermanently removes a note that is in the Trash, with its parts and history (CORE-N10).
func (s *Service) DeletePermanently(ctx context.Context, user, id uuid.UUID) error {
	return s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		n, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: id, UserID: user})
		if err != nil {
			return notFound(err)
		}
		if n.State != "deleted" {
			return ErrConflict // only notes in the Trash can be deleted for good
		}
		atts, err := s.attachmentsOf(ctx, tx, id)
		if err != nil {
			return err
		}
		// A reminder of the note that is waiting to be sent to a chat carries its text; it goes with the note (CR-012).
		if _, err := tx.Q.CancelOutboxForNote(ctx, dbq.CancelOutboxForNoteParams{Now: s.now(), UserID: uuid.NullUUID{UUID: user, Valid: true}, NoteID: id}); err != nil {
			return err
		}
		if _, err := tx.Q.DeleteNote(ctx, dbq.DeleteNoteParams{ID: id, UserID: user}); err != nil { // parts cascade
			return err
		}
		for _, a := range atts { // the files go with the note, and their space returns to the quota (CORE-A8)
			if err := s.Blobs.Delete(ctx, tx, a); err != nil {
				return err
			}
		}
		return tx.Change(ctx, "note", id, "delete", nil)
	})
}

// attachmentsOf lists the attachments used by a note's parts.
func (s *Service) attachmentsOf(ctx context.Context, tx *store.UserTx, note uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Q.AttachmentsOfNote(ctx, note)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

// ---- editing --------------------------------------------------------------------------------

// EditResult is the outcome of a text edit.
type EditResult struct {
	Note  Note
	Stale bool
}

// EditPart replaces the text of a part. The latest edit wins and nothing is lost: the previous
// text stays in the history (CORE-S4, EDT-5). Stale reports that the note changed since the
// version the client based the edit on, so the client can say so. Rapid edits of one part by one
// session are coalesced into a single history entry (CORE-N17).
func (s *Service) EditPart(ctx context.Context, user, noteID, partID uuid.UUID, text string, baseVersion *int32, session uuid.UUID) (EditResult, error) {
	text, err := validateText(text)
	if err != nil {
		return EditResult{}, err
	}
	now := s.now()
	var out EditResult
	err = s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		note, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: noteID, UserID: user})
		if err != nil {
			return notFound(err)
		}
		part, err := tx.Q.GetNotePart(ctx, dbq.GetNotePartParams{ID: partID, NoteID: noteID, UserID: user})
		if err != nil {
			return notFound(err)
		}
		if part.Kind != "text" {
			return invalid("only text parts can be edited")
		}
		out.Stale = baseVersion != nil && *baseVersion != note.Version
		if part.Text != nil && *part.Text == text {
			res, err := withParts(ctx, tx.Q, []dbq.Note{note})
			out.Note = res[0]
			return err
		}
		if err := tx.Q.UpdatePartText(ctx, dbq.UpdatePartTextParams{ID: partID, Text: &text, TextEditedAt: &now}); err != nil {
			return err
		}
		last, err := tx.Q.LatestAppVersion(ctx, partID)
		if err == nil && last.SessionID.Valid && last.SessionID.UUID == session && now.Sub(last.RecordedAt) < coalesceWindow {
			if err := tx.Q.OverwriteVersionText(ctx, dbq.OverwriteVersionTextParams{ID: last.ID, Text: text, EditedAt: now}); err != nil {
				return err
			}
		} else if err == nil || errors.Is(err, pgx.ErrNoRows) {
			vid, _ := uuid.NewV7()
			if err := tx.Q.InsertPartVersion(ctx, dbq.InsertPartVersionParams{ID: vid, UserID: user, PartID: partID, Text: text,
				Origin: "app", EditedAt: now, Applied: true, SessionID: uuid.NullUUID{UUID: session, Valid: session != uuid.Nil}}); err != nil {
				return err
			}
		} else {
			return err
		}
		n, err := tx.Q.TouchNote(ctx, dbq.TouchNoteParams{ID: noteID, UserID: user, UpdatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.Change(ctx, "note", noteID, "upsert", &n.Version); err != nil {
			return err
		}
		res, err := withParts(ctx, tx.Q, []dbq.Note{n})
		out.Note = res[0]
		return err
	})
	return out, err
}

// AddPart appends a text part to a note.
func (s *Service) AddPart(ctx context.Context, user, noteID uuid.UUID, in PartInput) (Note, error) {
	var text string
	if in.Text != nil && in.AttachmentID == nil {
		t, err := validateText(*in.Text)
		if err != nil {
			return Note{}, err
		}
		text = t
	} else if in.AttachmentID == nil {
		return Note{}, invalid("a part is either text or an attachment")
	}
	now := s.now()
	var out Note
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		if _, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: noteID, UserID: user}); err != nil {
			return notFound(err)
		}
		count, err := tx.Q.CountNoteParts(ctx, noteID)
		if err != nil {
			return err
		}
		if count >= MaxPartsPerNote {
			return invalid("too many parts")
		}
		ord, err := tx.Q.NextPartOrdinal(ctx, noteID)
		if err != nil {
			return err
		}
		if err := s.insertPart(ctx, tx, noteID, ord, in, text, now); err != nil {
			return err
		}
		n, err := tx.Q.TouchNote(ctx, dbq.TouchNoteParams{ID: noteID, UserID: user, UpdatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.Change(ctx, "note", noteID, "upsert", &n.Version); err != nil {
			return err
		}
		res, err := withParts(ctx, tx.Q, []dbq.Note{n})
		out = res[0]
		return err
	})
	return out, err
}

// RemovePart removes a part. A note always has at least one part (CORE-N1): to get rid of the
// last one, dismiss the note.
func (s *Service) RemovePart(ctx context.Context, user, noteID, partID uuid.UUID) (Note, error) {
	now := s.now()
	var out Note
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		if _, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: noteID, UserID: user}); err != nil {
			return notFound(err)
		}
		if _, err := tx.Q.GetNotePart(ctx, dbq.GetNotePartParams{ID: partID, NoteID: noteID, UserID: user}); err != nil {
			return notFound(err)
		}
		count, err := tx.Q.CountNoteParts(ctx, noteID)
		if err != nil {
			return err
		}
		if count <= 1 {
			return ErrConflict
		}
		part, err := tx.Q.GetNotePart(ctx, dbq.GetNotePartParams{ID: partID, NoteID: noteID, UserID: user})
		if err != nil {
			return notFound(err)
		}
		if err := tx.Q.DeleteNotePart(ctx, partID); err != nil {
			return err
		}
		if part.AttachmentID.Valid {
			if err := s.Blobs.Delete(ctx, tx, part.AttachmentID.UUID); err != nil {
				return err
			}
		}
		n, err := tx.Q.TouchNote(ctx, dbq.TouchNoteParams{ID: noteID, UserID: user, UpdatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.Change(ctx, "note", noteID, "upsert", &n.Version); err != nil {
			return err
		}
		res, err := withParts(ctx, tx.Q, []dbq.Note{n})
		out = res[0]
		return err
	})
	return out, err
}

// History returns every text version of every part of a note (EDT-3).
func (s *Service) History(ctx context.Context, user, noteID uuid.UUID) (map[uuid.UUID][]dbq.NotePartVersion, []uuid.UUID, error) {
	byPart := map[uuid.UUID][]dbq.NotePartVersion{}
	var order []uuid.UUID
	err := s.St.InUserRead(ctx, user, func(q *dbq.Queries) error {
		if _, err := q.GetNote(ctx, dbq.GetNoteParams{ID: noteID, UserID: user}); err != nil {
			return notFound(err)
		}
		rows, err := q.ListNoteVersions(ctx, noteID)
		if err != nil {
			return err
		}
		for _, v := range rows {
			if _, seen := byPart[v.PartID]; !seen {
				order = append(order, v.PartID)
			}
			byPart[v.PartID] = append(byPart[v.PartID], v)
		}
		return nil
	})
	return byPart, order, err
}

// ---- cursors --------------------------------------------------------------------------------

func encodeKeyCursor(key string, id uuid.UUID) string {
	return encodeCursorRaw(key + "|" + id.String())
}

func decodeKeyCursor(c string) (string, uuid.UUID, error) {
	raw, err := decodeCursorRaw(c)
	if err != nil {
		return "", uuid.Nil, err
	}
	key, idStr, ok := strings.Cut(raw, "|")
	if !ok || !fracindex.Valid(key) {
		return "", uuid.Nil, ErrInvalidCursor
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return "", uuid.Nil, ErrInvalidCursor
	}
	return key, id, nil
}

// Merge moves every part of source into target and removes source, to correct a grouping the app got
// wrong (CORE-N14, GRP-10). The parts keep their origin, so a later edit or deletion in the chat still
// finds them; the reminders of source move to target; links to source stop existing with it. Both notes
// must be active and belong to the caller.
func (s *Service) Merge(ctx context.Context, user, targetID, sourceID uuid.UUID) (Note, error) {
	if targetID == sourceID {
		return Note{}, invalid("a note cannot be merged into itself")
	}
	var out Note
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		target, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: targetID, UserID: user})
		if err != nil {
			return notFound(err)
		}
		source, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: sourceID, UserID: user})
		if err != nil {
			return notFound(err)
		}
		if target.State != "active" || source.State != "active" {
			return ErrConflict
		}
		offset, err := tx.Q.NextPartOrdinal(ctx, targetID)
		if err != nil {
			return err
		}
		if _, err := tx.Q.MovePartsToNote(ctx, dbq.MovePartsToNoteParams{UserID: user, NoteID: sourceID, NoteID_2: targetID, Ordinal: offset}); err != nil {
			return err
		}
		if _, err := tx.Q.MoveRemindersToNote(ctx, dbq.MoveRemindersToNoteParams{UserID: user, NoteID: sourceID, NoteID_2: targetID}); err != nil {
			return err
		}
		links, err := tx.Q.ShareLinkIDsOfNote(ctx, dbq.ShareLinkIDsOfNoteParams{UserID: user, NoteID: sourceID})
		if err != nil {
			return err
		}
		if _, err := tx.Q.DeleteNoteAnyState(ctx, dbq.DeleteNoteAnyStateParams{ID: sourceID, UserID: user}); err != nil {
			return err
		}
		for _, l := range links { // the links of the removed note went with it: tell clients which ones
			if err := tx.Change(ctx, "share_link", l, "delete", nil); err != nil {
				return err
			}
		}
		n, err := tx.Q.TouchNote(ctx, dbq.TouchNoteParams{ID: targetID, UserID: user, UpdatedAt: s.now()})
		if err != nil {
			return err
		}
		if err := tx.Change(ctx, "note", sourceID, "delete", nil); err != nil {
			return err
		}
		if err := tx.Change(ctx, "note", targetID, "upsert", &n.Version); err != nil {
			return err
		}
		res, err := withParts(ctx, tx.Q, []dbq.Note{n})
		if err != nil {
			return err
		}
		out = res[0]
		return nil
	})
	return out, err
}

// SplitResult is the note the part left and the note it now forms.
type SplitResult struct{ Source, Created Note }

// Split takes one part out of a note into a note of its own, placed right below it, with the part's
// own time as its creation time (CORE-N14, GRP-10). A note always keeps at least one part (CORE-N1).
func (s *Service) Split(ctx context.Context, user, noteID, partID uuid.UUID) (SplitResult, error) {
	var out SplitResult
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		src, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: noteID, UserID: user})
		if err != nil {
			return notFound(err)
		}
		if src.State != "active" {
			return ErrConflict
		}
		part, err := tx.Q.GetNotePart(ctx, dbq.GetNotePartParams{ID: partID, NoteID: noteID, UserID: user})
		if err != nil {
			return notFound(err)
		}
		if n, err := tx.Q.CountNoteParts(ctx, noteID); err != nil {
			return err
		} else if n < 2 {
			return invalid("a note with one part cannot be split")
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		var category uuid.NullUUID
		var key *string
		if src.CategoryID.Valid {
			k, err := s.NoteGroup(tx, src.CategoryID.UUID, id, s.now()).Place(ctx, position.Hints{AfterID: &noteID})
			if err != nil {
				return mapPosition(err)
			}
			category, key = src.CategoryID, &k
		}
		created, err := tx.Q.InsertNoteAtCreated(ctx, dbq.InsertNoteAtCreatedParams{ID: id, UserID: user, CategoryID: category, Position: key, CreatedAt: part.CreatedAt, ReceivedAt: s.now()})
		if err != nil {
			return err
		}
		if _, err := tx.Q.MovePartToNewNote(ctx, dbq.MovePartToNewNoteParams{UserID: user, ID: partID, NoteID: id}); err != nil {
			return err
		}
		touched, err := tx.Q.TouchNote(ctx, dbq.TouchNoteParams{ID: noteID, UserID: user, UpdatedAt: s.now()})
		if err != nil {
			return err
		}
		if err := tx.Change(ctx, "note", noteID, "upsert", &touched.Version); err != nil {
			return err
		}
		if err := tx.Change(ctx, "note", id, "upsert", &created.Version); err != nil {
			return err
		}
		res, err := withParts(ctx, tx.Q, []dbq.Note{touched, created})
		if err != nil {
			return err
		}
		out = SplitResult{Source: res[0], Created: res[1]}
		return nil
	})
	return out, err
}
