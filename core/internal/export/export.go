// Package export writes everything a user has into a ZIP archive, streaming (AUTH-U5, SEC-DATA-8,
// docs/design/02-api.md section 5). The archive holds `export.json` (pages, categories, notes with their
// parts, text history, reminders and share-link metadata without tokens) and every attachment as
// `attachments/<id>-<name>`. Memory stays bounded whatever the size: notes are read in batches, and
// each file is copied chunk by chunk.
package export

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/blobs"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// ErrRateLimited is returned when the user asked for too many exports lately.
var ErrRateLimited = errors.New("too many exports")

// PerHour is how many exports one user may start per hour.
const PerHour = 4

const batch = 500

// Service builds exports.
type Service struct {
	St    *store.Store
	Blobs *blobs.Service
	Now   func() time.Time
}

// New creates the service.
func New(st *store.Store, bl *blobs.Service) *Service {
	return &Service{St: st, Blobs: bl, Now: time.Now}
}

// Start checks the rate limit and records the export in the audit log, before anything is written.
func (s *Service) Start(ctx context.Context, actor store.Actor, user uuid.UUID) error {
	return s.St.InTx(ctx, func(q *dbq.Queries) error {
		n, err := q.CountRecentExports(ctx, dbq.CountRecentExportsParams{TargetID: uuid.NullUUID{UUID: user, Valid: true}, At: s.Now().Add(-time.Hour)})
		if err != nil {
			return err
		}
		if n >= PerHour {
			return ErrRateLimited
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "user.exported", TargetKind: "user", TargetID: &user})
	})
}

type partOut struct {
	ID            uuid.UUID    `json:"id"`
	Kind          string       `json:"kind"`
	Text          *string      `json:"text,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	TextEditedAt  *time.Time   `json:"text_edited_at,omitempty"`
	AttachReason  string       `json:"attach_reason"`
	Attachment    *attachedOut `json:"attachment,omitempty"`
	FailedFile    *string      `json:"failed_filename,omitempty"`
	FailedReason  *string      `json:"failed_reason,omitempty"`
	FailedSize    *int64       `json:"failed_size,omitempty"`
	SourceMessage *string      `json:"source_message_id,omitempty"`
	History       []versionOut `json:"history,omitempty"`
}

type attachedOut struct {
	ID        uuid.UUID `json:"id"`
	Filename  string    `json:"filename"`
	MediaType string    `json:"media_type"`
	Size      int64     `json:"size"`
	File      string    `json:"file"` // the path inside the archive
}

type versionOut struct {
	Text     string    `json:"text"`
	Origin   string    `json:"origin"`
	EditedAt time.Time `json:"edited_at"`
	Applied  bool      `json:"applied"`
}

type noteOut struct {
	ID         uuid.UUID  `json:"id"`
	State      string     `json:"state"`
	CategoryID *uuid.UUID `json:"category_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
	Parts      []partOut  `json:"parts"`
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '/' || r == '\\' || r == ':' || r == 0x7f {
			return '_'
		}
		return r
	}, s)
	s = strings.Trim(s, " .")
	if s == "" {
		s = "file"
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// Write streams the archive to w. Everything is read under the user's own row-level security, so it
// cannot contain anything that is not theirs (SEC-DATA-8).
func (s *Service) Write(ctx context.Context, user uuid.UUID, w io.Writer, profile map[string]any) error {
	zw := zip.NewWriter(w)
	js, err := zw.CreateHeader(&zip.FileHeader{Name: "export.json", Method: zip.Deflate, Modified: s.Now()})
	if err != nil {
		return err
	}
	var files []attachedOut
	err = s.St.InUserRead(ctx, user, func(q *dbq.Queries) error {
		enc := func(v any) error { return json.NewEncoder(js).Encode(v) }
		write := func(str string) error { _, err := io.WriteString(js, str); return err }
		if err := write(fmt.Sprintf(`{"format":"notekeeper-export","version":1,"exported_at":%q,`, s.Now().UTC().Format(time.RFC3339))); err != nil {
			return err
		}
		if err := write(`"profile":`); err != nil {
			return err
		}
		if err := enc(profile); err != nil {
			return err
		}
		pages, err := q.ExportPages(ctx, user)
		if err != nil {
			return err
		}
		if err := write(`,"pages":`); err != nil {
			return err
		}
		if err := enc(pages); err != nil {
			return err
		}
		cats, err := q.ExportCategories(ctx, user)
		if err != nil {
			return err
		}
		if err := write(`,"categories":`); err != nil {
			return err
		}
		if err := enc(cats); err != nil {
			return err
		}
		if err := write(`,"notes":[`); err != nil {
			return err
		}
		first := true
		after := uuid.Nil
		for {
			notes, err := q.ExportNotes(ctx, dbq.ExportNotesParams{UserID: user, ID: after, Limit: batch})
			if err != nil {
				return err
			}
			if len(notes) == 0 {
				break
			}
			ids := make([]uuid.UUID, len(notes))
			for i, n := range notes {
				ids[i] = n.ID
			}
			after = ids[len(ids)-1]
			parts, err := q.ExportParts(ctx, dbq.ExportPartsParams{UserID: user, Ids: ids})
			if err != nil {
				return err
			}
			versions, err := q.ExportVersions(ctx, dbq.ExportVersionsParams{UserID: user, Ids: ids})
			if err != nil {
				return err
			}
			var attIDs []uuid.UUID
			for _, p := range parts {
				if p.AttachmentID.Valid {
					attIDs = append(attIDs, p.AttachmentID.UUID)
				}
			}
			info := map[uuid.UUID]dbq.ExportAttachmentInfoRow{}
			if len(attIDs) > 0 {
				rows, err := q.ExportAttachmentInfo(ctx, dbq.ExportAttachmentInfoParams{UserID: user, Ids: attIDs})
				if err != nil {
					return err
				}
				for _, r := range rows {
					info[r.ID] = r
				}
			}
			hist := map[uuid.UUID][]versionOut{}
			for _, v := range versions {
				hist[v.PartID] = append(hist[v.PartID], versionOut{Text: v.Text, Origin: v.Origin, EditedAt: v.EditedAt, Applied: v.Applied})
			}
			byNote := map[uuid.UUID][]partOut{}
			for _, p := range parts {
				out := partOut{ID: p.ID, Kind: p.Kind, Text: p.Text, CreatedAt: p.CreatedAt, TextEditedAt: p.TextEditedAt, AttachReason: p.AttachReason,
					FailedFile: p.FailedFilename, FailedReason: p.FailedReason, FailedSize: p.FailedSize, SourceMessage: p.SourceMessageID, History: hist[p.ID]}
				if p.AttachmentID.Valid {
					if a, ok := info[p.AttachmentID.UUID]; ok {
						ao := attachedOut{ID: a.ID, Filename: a.Filename, MediaType: a.MediaType, Size: a.SizeBytes, File: "attachments/" + a.ID.String() + "-" + safeName(a.Filename)}
						out.Attachment = &ao
						files = append(files, ao)
					}
				}
				byNote[p.NoteID] = append(byNote[p.NoteID], out)
			}
			for _, n := range notes {
				no := noteOut{ID: n.ID, State: n.State, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, DeletedAt: n.DeletedAt, Parts: byNote[n.ID]}
				if n.CategoryID.Valid {
					c := n.CategoryID.UUID
					no.CategoryID = &c
				}
				if !first {
					if err := write(","); err != nil {
						return err
					}
				}
				first = false
				if err := enc(no); err != nil {
					return err
				}
			}
		}
		if err := write(`],"reminders":`); err != nil {
			return err
		}
		rems, err := q.ExportReminders(ctx, user)
		if err != nil {
			return err
		}
		if err := enc(rems); err != nil {
			return err
		}
		links, err := q.ExportShareLinks(ctx, user)
		if err != nil {
			return err
		}
		if err := write(`,"share_links":`); err != nil {
			return err
		}
		if err := enc(links); err != nil { // metadata only: the tokens are not stored and cannot be exported
			return err
		}
		return write("}\n")
	})
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := s.copyFile(ctx, zw, user, f); err != nil {
			return err
		}
	}
	return zw.Close()
}

func (s *Service) copyFile(ctx context.Context, zw *zip.Writer, user uuid.UUID, f attachedOut) error {
	r, _, err := s.Blobs.Open(ctx, user, f.ID)
	if err != nil {
		return err
	}
	fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.File, Method: zip.Store, Modified: s.Now()}) // already compressed for the most part
	if err != nil {
		return err
	}
	_, err = io.Copy(fw, r)
	return err
}
