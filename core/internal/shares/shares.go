// Package shares implements read-only share links to a single note (docs/design/01-data-model.md
// section 10, requirements CORE-SH1..SH14). A link is a random token whose hash is stored; whether
// it works is decided on every request by a live check, never by a stored flag, so dismissing a
// note, revoking a link or disabling its owner takes effect at once.
package shares

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/blobs"
	"github.com/Niboor/notekeeper/core/internal/notes"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// Errors.
var (
	ErrNotFound  = store.ErrNotFound
	ErrDisabled  = errors.New("sharing is switched off")
	ErrInvalid   = errors.New("invalid expiry")
	ErrNotActive = errors.New("only a note that is not in the Trash can be shared")
	ErrLimit     = errors.New("too many share links")
)

// Presets are the lifetimes a person may choose (CORE-SH2). The operator's maximum can lower them.
var Presets = map[string]time.Duration{"1h": time.Hour, "1d": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

// Config holds the operator's limits.
type Config struct {
	Enabled     bool
	MaxLifetime time.Duration
	MaxActive   int // links one user may hold at once (SEC-SHR-11)
	// CreatedPerHour bounds how fast one user can mint links, stolen sessions included (SEC-SHR-11).
	CreatedPerHour int
}

// Service manages links.
type Service struct {
	St    *store.Store
	Notes *notes.Service
	Blobs *blobs.Service
	Cfg   Config
	Now   func() time.Time
}

// New creates the service.
func New(st *store.Store, n *notes.Service, bl *blobs.Service, cfg Config) *Service {
	if cfg.CreatedPerHour == 0 {
		cfg.CreatedPerHour = 30
	}
	return &Service{St: st, Notes: n, Blobs: bl, Cfg: cfg, Now: time.Now}
}

// tokenBytes is 24 random bytes: 192 bits, above the 128 the requirements ask for (CORE-SH6).
const tokenBytes = 24

// TokenLength is the length of a token in its URL-safe form.
const TokenLength = 32

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func validToken(token string) bool {
	if len(token) != TokenLength {
		return false
	}
	for _, r := range token {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Link is what the owner sees of a link.
type Link struct {
	ID             uuid.UUID
	NoteID         uuid.UUID
	Excerpt        string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	LastAccessedAt *time.Time
	ViewCount      int32
	NoteActive     bool
}

// Actor is who performs an action, for the audit log.
type Actor = store.Actor

// Create makes a link for a note the user owns. The token is returned once; only its hash is kept.
func (s *Service) Create(ctx context.Context, actor Actor, user, note uuid.UUID, preset string) (Link, string, error) {
	if !s.Cfg.Enabled {
		return Link{}, "", ErrDisabled
	}
	life, ok := Presets[preset]
	if !ok || life > s.Cfg.MaxLifetime {
		return Link{}, "", ErrInvalid // enforced here, not only in the UI (SEC-SHR-2)
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return Link{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.Now()
	id, err := uuid.NewV7()
	if err != nil {
		return Link{}, "", err
	}
	var link Link
	err = s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		n, err := tx.Q.GetNote(ctx, dbq.GetNoteParams{ID: note, UserID: user})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound // also for a note that belongs to somebody else (SEC-SHR-9)
		}
		if err != nil {
			return err
		}
		if n.State != "active" {
			return ErrNotActive
		}
		active, err := tx.Q.CountActiveShareLinks(ctx, dbq.CountActiveShareLinksParams{UserID: user, ExpiresAt: now})
		if err != nil {
			return err
		}
		recent, err := tx.Q.CountShareLinksCreatedSince(ctx, dbq.CountShareLinksCreatedSinceParams{UserID: user, CreatedAt: now.Add(-time.Hour)})
		if err != nil {
			return err
		}
		if int(active) >= s.Cfg.MaxActive || int(recent) >= s.Cfg.CreatedPerHour {
			return ErrLimit
		}
		row, err := tx.Q.InsertShareLink(ctx, dbq.InsertShareLinkParams{ID: id, UserID: user, NoteID: note, TokenHash: hashToken(token),
			CreatedAt: now, ExpiresAt: now.Add(life)})
		if err != nil {
			return err
		}
		link = Link{ID: row.ID, NoteID: row.NoteID, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, NoteActive: true}
		if err := tx.Change(ctx, "share_link", id, "upsert", nil); err != nil {
			return err
		}
		return store.Audit(ctx, tx.Q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "share.created", TargetKind: "note", TargetID: &note,
			Detail: map[string]any{"link_id": id, "expires_in": preset}}) // no content, and never the token (CORE-SH12)
	})
	if err != nil {
		return Link{}, "", err
	}
	if n, err := s.excerptOf(ctx, user, note); err == nil {
		link.Excerpt = n
	}
	return link, token, nil
}

func (s *Service) excerptOf(ctx context.Context, user, note uuid.UUID) (string, error) {
	links, err := s.List(ctx, user, &note)
	if err != nil || len(links) == 0 {
		return "", err
	}
	return links[0].Excerpt, nil
}

// List returns the user's links that have not expired or been revoked, newest first, for one note or all (CORE-SH3).
func (s *Service) List(ctx context.Context, user uuid.UUID, note *uuid.UUID) ([]Link, error) {
	var out []Link
	err := s.St.InUserRead(ctx, user, func(q *dbq.Queries) error {
		p := dbq.ListShareLinksParams{UserID: user, Now: s.Now()}
		if note != nil {
			p.NoteID = uuid.NullUUID{UUID: *note, Valid: true}
		}
		rows, err := q.ListShareLinks(ctx, p)
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, Link{ID: r.ID, NoteID: r.NoteID, Excerpt: r.Excerpt, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
				LastAccessedAt: r.LastAccessedAt, ViewCount: r.ViewCount, NoteActive: r.NoteState == "active"})
		}
		return nil
	})
	return out, err
}

// Revoke ends a link at once (CORE-SH3). A link that is not the user's, or already over, is not found.
func (s *Service) Revoke(ctx context.Context, actor Actor, user, id uuid.UUID) error {
	return s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		row, err := tx.Q.RevokeShareLink(ctx, dbq.RevokeShareLinkParams{ID: id, UserID: user, RevokedAt: sPtr(s.Now())})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := tx.Change(ctx, "share_link", id, "delete", nil); err != nil {
			return err
		}
		return store.Audit(ctx, tx.Q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "share.revoked", TargetKind: "note", TargetID: &row.NoteID,
			Detail: map[string]any{"link_id": id}})
	})
}

// RevokeAll ends every link of the user (CORE-SH14).
func (s *Service) RevokeAll(ctx context.Context, actor Actor, user uuid.UUID) (int, error) {
	var n int
	err := s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		ids, err := tx.Q.RevokeAllShareLinks(ctx, dbq.RevokeAllShareLinksParams{UserID: user, RevokedAt: sPtr(s.Now())})
		if err != nil {
			return err
		}
		n = len(ids)
		for _, id := range ids {
			if err := tx.Change(ctx, "share_link", id, "delete", nil); err != nil {
				return err
			}
		}
		if n == 0 {
			return nil
		}
		return store.Audit(ctx, tx.Q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "share.revoked_all", TargetKind: "user", TargetID: &user,
			Detail: map[string]any{"count": n}})
	})
	return n, err
}

// Purge deletes links that ended more than a grace period ago (docs/design/05, PurgeShareLinks).
func (s *Service) Purge(ctx context.Context, grace time.Duration) (int64, error) {
	cutoff := s.Now().Add(-grace)
	return s.St.Q().PurgeShareLinks(ctx, cutoff)
}

// ---- the public side ----------------------------------------------------------------------------

// Shared is what a link shows: the note's current content and nothing else (CORE-SH4, SEC-SHR-4).
type Shared struct {
	CreatedAt time.Time
	ExpiresAt time.Time
	Parts     []SharedPart
}

// SharedPart is one part of a shared note.
type SharedPart struct {
	Kind           string // text | attachment | failed_attachment | unsupported
	Text           string
	Attachment     *blobs.Attachment
	FailedFilename string
}

// resolved is a link that passed the live check.
type resolved struct {
	linkID uuid.UUID
	user   uuid.UUID
	note   uuid.UUID
	expiry time.Time
}

// resolve applies the live validity check (CORE-SH5, CORE-SH6): every failure, whatever the
// reason, is the same ErrNotFound, so a token cannot be probed for "expired" or "revoked".
func (s *Service) resolve(ctx context.Context, token string) (resolved, error) {
	if !s.Cfg.Enabled || !validToken(token) {
		return resolved{}, ErrNotFound
	}
	row, err := s.St.Q().LookupShare(ctx, dbq.LookupShareParams{TokenHash: hashToken(token), ExpiresAt: s.Now()})
	if errors.Is(err, pgx.ErrNoRows) {
		return resolved{}, ErrNotFound
	}
	if err != nil {
		return resolved{}, err
	}
	return resolved{linkID: row.ID, user: row.UserID, note: row.NoteID, expiry: row.ExpiresAt}, nil
}

// LinkKey identifies a link for rate limiting without revealing the token.
func LinkKey(token string) string { return fmt.Sprintf("%x", hashToken(token)[:8]) }

// Open returns the shared note's current content and counts the view (CORE-SH3, CORE-SH12).
func (s *Service) Open(ctx context.Context, token string) (Shared, error) {
	r, err := s.resolve(ctx, token)
	if err != nil {
		return Shared{}, err
	}
	n, err := s.Notes.Get(ctx, r.user, r.note)
	if err != nil {
		return Shared{}, err // a missing note is ErrNotFound like everything else
	}
	if n.Note.State != "active" {
		return Shared{}, ErrNotFound // a note in the Trash switches its links off (CORE-SH5)
	}
	out := Shared{CreatedAt: n.Note.CreatedAt, ExpiresAt: r.expiry}
	for _, p := range n.Parts {
		sp := SharedPart{Kind: p.Kind}
		switch p.Kind {
		case "text", "unsupported":
			if p.Text != nil {
				sp.Text = *p.Text
			}
		case "attachment":
			if p.Attachment == nil {
				continue
			}
			sp.Attachment = &blobs.Attachment{ID: p.Attachment.ID, Filename: p.Attachment.Filename, MediaType: p.Attachment.MediaType, Size: p.Attachment.Size}
		case "failed_attachment":
			if p.FailedFilename != nil {
				sp.FailedFilename = *p.FailedFilename
			}
		}
		out.Parts = append(out.Parts, sp)
	}
	// Counting is best effort: a failure to count must not hide the note.
	_ = s.St.Q().TouchShareLink(ctx, dbq.TouchShareLinkParams{ID: r.linkID, LastAccessedAt: sPtr(s.Now())})
	return out, nil
}

// OpenAttachment opens one file of the shared note. A file that is not a part of that note is not
// found, whoever asks and however the id was obtained (CORE-SH7, SEC-SHR-4).
func (s *Service) OpenAttachment(ctx context.Context, token string, attachment uuid.UUID) (*blobs.Reader, blobs.Attachment, error) {
	r, err := s.resolve(ctx, token)
	if err != nil {
		return nil, blobs.Attachment{}, err
	}
	err = s.St.InUserRead(ctx, r.user, func(q *dbq.Queries) error {
		n, err := q.GetNote(ctx, dbq.GetNoteParams{ID: r.note, UserID: r.user})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && n.State != "active") {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		has, err := q.NoteHasAttachment(ctx, dbq.NoteHasAttachmentParams{UserID: r.user, NoteID: r.note, AttachmentID: uuid.NullUUID{UUID: attachment, Valid: true}})
		if err != nil {
			return err
		}
		if !has {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, blobs.Attachment{}, err
	}
	return s.Blobs.Open(ctx, r.user, attachment)
}

func sPtr[T any](v T) *T { return &v }
