// Package board implements pages and categories and the board view that shows a page's
// categories with their first notes (docs/design/02-api.md section 1.2).
package board

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Niboor/notekeeper/core/internal/fracindex"
	"github.com/Niboor/notekeeper/core/internal/notes"
	"github.com/Niboor/notekeeper/core/internal/position"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// Errors returned by the service.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrConflict     = errors.New("conflict")
)

// Limits (SEC-API-3): a single user can only make so much.
const (
	MaxPages      = 100
	MaxCategories = 200 // per page
)

// Service manages pages and categories.
type Service struct {
	St    *store.Store
	Notes *notes.Service
	Now   func() time.Time
}

// New creates the service.
func New(st *store.Store, n *notes.Service) *Service {
	return &Service{St: st, Notes: n, Now: time.Now}
}

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidInput, msg) }

func keyOr(err error) (string, bool, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return "", false, err
}

func opt(key string, err error) (string, bool, error) {
	if err != nil {
		return keyOr(err)
	}
	return key, true, nil
}

// ---- position groups ------------------------------------------------------------------------

func pageGroup(tx *store.UserTx, exclude uuid.UUID, now time.Time) position.Group {
	u := tx.UserID
	return position.Group{
		KeyOf: func(ctx context.Context, id uuid.UUID) (string, bool, error) {
			p, err := tx.Q.GetPage(ctx, dbq.GetPageParams{ID: id, UserID: u})
			if errors.Is(err, pgx.ErrNoRows) || id == exclude {
				return "", false, nil
			}
			return p.Position, err == nil, err
		},
		After: func(ctx context.Context, k string) (string, bool, error) {
			return opt(tx.Q.PageKeyAfter(ctx, dbq.PageKeyAfterParams{UserID: u, ID: exclude, Position: k}))
		},
		Before: func(ctx context.Context, k string) (string, bool, error) {
			return opt(tx.Q.PageKeyBefore(ctx, dbq.PageKeyBeforeParams{UserID: u, ID: exclude, Position: k}))
		},
		First: func(ctx context.Context) (string, bool, error) {
			return opt(tx.Q.PageKeyFirst(ctx, dbq.PageKeyFirstParams{UserID: u, ID: exclude}))
		},
		Last: func(ctx context.Context) (string, bool, error) {
			return opt(tx.Q.PageKeyLast(ctx, dbq.PageKeyLastParams{UserID: u, ID: exclude}))
		},
		Rebalance: func(ctx context.Context) error {
			ids, err := tx.Q.PageIDsOrdered(ctx, dbq.PageIDsOrderedParams{UserID: u, ID: exclude})
			if err != nil {
				return err
			}
			for i, k := range fracindex.Even(len(ids)) {
				v, err := tx.Q.RekeyPage(ctx, dbq.RekeyPageParams{ID: ids[i], UserID: u, Position: k, UpdatedAt: now})
				if err != nil {
					return err
				}
				if err := tx.Change(ctx, "page", ids[i], "upsert", &v); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func categoryGroup(tx *store.UserTx, page, exclude uuid.UUID, now time.Time) position.Group {
	u := tx.UserID
	return position.Group{
		KeyOf: func(ctx context.Context, id uuid.UUID) (string, bool, error) {
			c, err := tx.Q.GetCategory(ctx, dbq.GetCategoryParams{ID: id, UserID: u})
			if errors.Is(err, pgx.ErrNoRows) || id == exclude || (err == nil && c.PageID != page) {
				return "", false, nil
			}
			return c.Position, err == nil, err
		},
		After: func(ctx context.Context, k string) (string, bool, error) {
			return opt(tx.Q.CategoryKeyAfter(ctx, dbq.CategoryKeyAfterParams{UserID: u, PageID: page, ID: exclude, Position: k}))
		},
		Before: func(ctx context.Context, k string) (string, bool, error) {
			return opt(tx.Q.CategoryKeyBefore(ctx, dbq.CategoryKeyBeforeParams{UserID: u, PageID: page, ID: exclude, Position: k}))
		},
		First: func(ctx context.Context) (string, bool, error) {
			return opt(tx.Q.CategoryKeyFirst(ctx, dbq.CategoryKeyFirstParams{UserID: u, PageID: page, ID: exclude}))
		},
		Last: func(ctx context.Context) (string, bool, error) {
			return opt(tx.Q.CategoryKeyLast(ctx, dbq.CategoryKeyLastParams{UserID: u, PageID: page, ID: exclude}))
		},
		Rebalance: func(ctx context.Context) error {
			ids, err := tx.Q.CategoryIDsOrdered(ctx, dbq.CategoryIDsOrderedParams{UserID: u, PageID: page, ID: exclude})
			if err != nil {
				return err
			}
			for i, k := range fracindex.Even(len(ids)) {
				v, err := tx.Q.RekeyCategory(ctx, dbq.RekeyCategoryParams{ID: ids[i], UserID: u, Position: k, UpdatedAt: now})
				if err != nil {
					return err
				}
				if err := tx.Change(ctx, "category", ids[i], "upsert", &v); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// ---- pages ----------------------------------------------------------------------------------

// ListPages returns the user's pages in order.
func (s *Service) ListPages(ctx context.Context, user uuid.UUID) ([]dbq.Page, error) {
	var out []dbq.Page
	err := s.St.InUserRead(ctx, user, func(q *dbq.Queries) error {
		var err error
		out, err = q.ListPages(ctx, user)
		return err
	})
	return out, err
}

// CreatePage creates a page, by default after the last one. Repeating a client-generated id
// returns the existing page (CORE-S5).
func (s *Service) CreatePage(ctx context.Context, user uuid.UUID, id *uuid.UUID, name string, after *uuid.UUID) (dbq.Page, error) {
	name, err := cleanName(name)
	if err != nil {
		return dbq.Page{}, err
	}
	now := s.Now()
	var out dbq.Page
	err = s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		pid := uuid.Nil
		if id != nil {
			pid = *id
			if existing, err := tx.Q.GetPage(ctx, dbq.GetPageParams{ID: pid, UserID: user}); err == nil {
				if existing.Name != name {
					return ErrConflict
				}
				out = existing
				return nil
			}
		} else {
			pid, _ = uuid.NewV7()
		}
		pages, err := tx.Q.ListPages(ctx, user)
		if err != nil {
			return err
		}
		if len(pages) >= MaxPages {
			return invalid("too many pages")
		}
		key, err := pageGroup(tx, uuid.Nil, now).Place(ctx, position.Hints{AfterID: after})
		if err != nil {
			return mapPosition(err)
		}
		if out, err = tx.Q.InsertPage(ctx, dbq.InsertPageParams{ID: pid, UserID: user, Name: name, Position: key}); err != nil {
			if store.IsUniqueViolation(err, "") {
				return ErrConflict // an id in use by someone else looks the same as a conflicting repeat
			}
			return err
		}
		return tx.Change(ctx, "page", out.ID, "upsert", &out.Version)
	})
	return out, err
}

// PageUpdate lists the fields a page update may change.
type PageUpdate struct {
	Name              *string
	Archived          *bool
	BeforeID, AfterID *uuid.UUID
}

// UpdatePage renames, reorders or archives a page.
func (s *Service) UpdatePage(ctx context.Context, user, id uuid.UUID, u PageUpdate) (dbq.Page, error) {
	now := s.Now()
	var out dbq.Page
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		if _, err := tx.Q.GetPage(ctx, dbq.GetPageParams{ID: id, UserID: user}); err != nil {
			return notFound(err)
		}
		params := dbq.UpdatePageParams{ID: id, UserID: user, Now: now}
		if u.Archived != nil {
			params.Archived = pgtype.Bool{Bool: *u.Archived, Valid: true}
		}
		if u.Name != nil {
			n, err := cleanName(*u.Name)
			if err != nil {
				return err
			}
			params.Name = &n
		}
		if u.BeforeID != nil || u.AfterID != nil {
			key, err := pageGroup(tx, id, now).Place(ctx, position.Hints{AfterID: u.AfterID, BeforeID: u.BeforeID})
			if err != nil {
				return mapPosition(err)
			}
			params.Position = &key
		}
		var err error
		if out, err = tx.Q.UpdatePage(ctx, params); err != nil {
			return err
		}
		return tx.Change(ctx, "page", id, "upsert", &out.Version)
	})
	return out, err
}

// DeletePage deletes a page. Notes in its categories return to the Inbox and are never lost
// (CORE-P4); the number of notes moved is returned so the user can be told.
func (s *Service) DeletePage(ctx context.Context, user, id uuid.UUID) (int, error) {
	moved := 0
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		if _, err := tx.Q.GetPage(ctx, dbq.GetPageParams{ID: id, UserID: user}); err != nil {
			return notFound(err)
		}
		cats, err := tx.Q.CategoryIDsOfPage(ctx, dbq.CategoryIDsOfPageParams{UserID: user, PageID: id})
		if err != nil {
			return err
		}
		if moved, err = s.releaseNotes(ctx, tx, cats); err != nil {
			return err
		}
		if _, err := tx.Q.DeletePage(ctx, dbq.DeletePageParams{ID: id, UserID: user}); err != nil {
			return err
		}
		for _, c := range cats {
			if err := tx.Change(ctx, "category", c, "delete", nil); err != nil {
				return err
			}
		}
		return tx.Change(ctx, "page", id, "delete", nil)
	})
	return moved, err
}

// releaseNotes returns the notes of the given categories to the Inbox before the categories go,
// and records a change for each so every client sees them arrive (the database only nulls their
// category through its foreign key, which by itself would leave clients unaware). It returns the
// number of active notes affected; dismissed notes are also freed and come back to the Inbox on
// restore (CORE-P4).
func (s *Service) releaseNotes(ctx context.Context, tx *store.UserTx, categories []uuid.UUID) (int, error) {
	if len(categories) == 0 {
		return 0, nil
	}
	ids, err := tx.Q.NoteIDsInCategories(ctx, dbq.NoteIDsInCategoriesParams{UserID: tx.UserID, Column2: categories})
	if err != nil {
		return 0, err
	}
	active := 0
	for _, id := range ids {
		n, err := tx.Q.SetNoteLocation(ctx, dbq.SetNoteLocationParams{ID: id, UserID: tx.UserID, UpdatedAt: s.Now()})
		if err != nil {
			return 0, err
		}
		if n.State == "active" {
			active++
		}
		if err := tx.Change(ctx, "note", id, "upsert", &n.Version); err != nil {
			return 0, err
		}
	}
	return active, nil
}

// ---- categories -----------------------------------------------------------------------------

// CreateCategory creates a category on a page, by default after the last one.
func (s *Service) CreateCategory(ctx context.Context, user uuid.UUID, id *uuid.UUID, page uuid.UUID, name string, after *uuid.UUID) (dbq.Category, error) {
	name, err := cleanName(name)
	if err != nil {
		return dbq.Category{}, err
	}
	now := s.Now()
	var out dbq.Category
	err = s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		if _, err := tx.Q.GetPage(ctx, dbq.GetPageParams{ID: page, UserID: user}); err != nil {
			return notFound(err)
		}
		cid := uuid.Nil
		if id != nil {
			cid = *id
			if existing, err := tx.Q.GetCategory(ctx, dbq.GetCategoryParams{ID: cid, UserID: user}); err == nil {
				if existing.Name != name || existing.PageID != page {
					return ErrConflict
				}
				out = existing
				return nil
			}
		} else {
			cid, _ = uuid.NewV7()
		}
		cats, err := tx.Q.ListCategoriesOfPage(ctx, dbq.ListCategoriesOfPageParams{UserID: user, PageID: page})
		if err != nil {
			return err
		}
		if len(cats) >= MaxCategories {
			return invalid("too many categories on this page")
		}
		key, err := categoryGroup(tx, page, uuid.Nil, now).Place(ctx, position.Hints{AfterID: after})
		if err != nil {
			return mapPosition(err)
		}
		if out, err = tx.Q.InsertCategory(ctx, dbq.InsertCategoryParams{ID: cid, UserID: user, PageID: page, Name: name, Position: key}); err != nil {
			if store.IsUniqueViolation(err, "") {
				return ErrConflict
			}
			return err
		}
		return tx.Change(ctx, "category", out.ID, "upsert", &out.Version)
	})
	return out, err
}

// CategoryUpdate lists the fields a category update may change.
type CategoryUpdate struct {
	Name              *string
	PageID            *uuid.UUID
	BeforeID, AfterID *uuid.UUID
}

// UpdateCategory renames, reorders or moves a category to another page.
func (s *Service) UpdateCategory(ctx context.Context, user, id uuid.UUID, u CategoryUpdate) (dbq.Category, error) {
	now := s.Now()
	var out dbq.Category
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		cur, err := tx.Q.GetCategory(ctx, dbq.GetCategoryParams{ID: id, UserID: user})
		if err != nil {
			return notFound(err)
		}
		params := dbq.UpdateCategoryParams{ID: id, UserID: user, Now: now}
		if u.Name != nil {
			n, err := cleanName(*u.Name)
			if err != nil {
				return err
			}
			params.Name = &n
		}
		targetPage := cur.PageID
		if u.PageID != nil && *u.PageID != cur.PageID {
			if _, err := tx.Q.GetPage(ctx, dbq.GetPageParams{ID: *u.PageID, UserID: user}); err != nil {
				return notFound(err) // a foreign or missing page: same answer (SEC-ISO-4)
			}
			targetPage = *u.PageID
			params.PageID = uuid.NullUUID{UUID: targetPage, Valid: true}
		}
		if u.BeforeID != nil || u.AfterID != nil || targetPage != cur.PageID {
			key, err := categoryGroup(tx, targetPage, id, now).Place(ctx, position.Hints{AfterID: u.AfterID, BeforeID: u.BeforeID})
			if err != nil {
				return mapPosition(err)
			}
			params.Position = &key
		}
		if out, err = tx.Q.UpdateCategory(ctx, params); err != nil {
			return err
		}
		return tx.Change(ctx, "category", id, "upsert", &out.Version)
	})
	return out, err
}

// DeleteCategory deletes a category; its notes return to the Inbox (CORE-P4).
func (s *Service) DeleteCategory(ctx context.Context, user, id uuid.UUID) (int, error) {
	moved := 0
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		if _, err := tx.Q.GetCategory(ctx, dbq.GetCategoryParams{ID: id, UserID: user}); err != nil {
			return notFound(err)
		}
		var err error
		if moved, err = s.releaseNotes(ctx, tx, []uuid.UUID{id}); err != nil {
			return err
		}
		if _, err := tx.Q.DeleteCategory(ctx, dbq.DeleteCategoryParams{ID: id, UserID: user}); err != nil {
			return err
		}
		return tx.Change(ctx, "category", id, "delete", nil)
	})
	return moved, err
}

// ---- board ----------------------------------------------------------------------------------

// BoardCategory is one column of the board.
type BoardCategory struct {
	Category   dbq.Category
	Notes      []notes.Note
	Total      int64
	NextCursor string
}

// Board is a page with its categories and their first notes, fetched in one call so the initial
// view of a page costs a single request (WEB-N3).
type Board struct {
	Page       dbq.Page
	Categories []BoardCategory
	InboxTotal int64
}

// GetBoard returns a page's columns with the first perCategory notes of each.
func (s *Service) GetBoard(ctx context.Context, user, page uuid.UUID, perCategory int) (Board, error) {
	if perCategory <= 0 || perCategory > 100 {
		perCategory = 30
	}
	var out Board
	err := s.St.InUserRead(ctx, user, func(q *dbq.Queries) error {
		p, err := q.GetPage(ctx, dbq.GetPageParams{ID: page, UserID: user})
		if err != nil {
			return notFound(err)
		}
		out.Page = p
		if out.InboxTotal, err = q.CountInbox(ctx, user); err != nil {
			return err
		}
		cats, err := q.ListCategoriesOfPage(ctx, dbq.ListCategoriesOfPageParams{UserID: user, PageID: page})
		if err != nil {
			return err
		}
		for _, c := range cats {
			bc := BoardCategory{Category: c}
			if bc.Total, err = q.CountCategoryNotes(ctx, dbq.CountCategoryNotesParams{UserID: user, CategoryID: uuid.NullUUID{UUID: c.ID, Valid: true}}); err != nil {
				return err
			}
			pg, err := s.Notes.CategoryPage(ctx, q, user, c.ID, perCategory, "")
			if err != nil {
				return err
			}
			bc.Notes, bc.NextCursor = pg.Items, pg.NextCursor
			out.Categories = append(out.Categories, bc)
		}
		return nil
	})
	return out, err
}

// ---- helpers --------------------------------------------------------------------------------

func cleanName(name string) (string, error) {
	n := []rune(trim(name))
	if len(n) == 0 || len(n) > 100 {
		return "", invalid("name must be 1 to 100 characters")
	}
	return string(n), nil
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n') {
		s = s[:len(s)-1]
	}
	return s
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

func mapPosition(err error) error {
	if errors.Is(err, position.ErrNeighbour) {
		return store.ErrNotFound // a foreign neighbour looks like a missing one
	}
	return err
}
