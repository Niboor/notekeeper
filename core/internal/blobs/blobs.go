// Package blobs stores attachments in PostgreSQL as fixed-size chunks, streams them in and out
// with memory bounded by one chunk, and enforces the per-user quota atomically
// (docs/design/01-data-model.md section 6). All access goes through this package so that a
// different storage backend can replace the chunk table later without an API change (CORE-A2,
// CORE-A7): the blob row names its backend and every read looks at it.
package blobs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// Errors.
var (
	ErrQuota         = errors.New("storage quota exceeded")
	ErrTooLarge      = errors.New("attachment too large")
	ErrEmpty         = errors.New("empty attachment")
	ErrLength        = errors.New("body does not match the declared length")
	ErrUploadRunning = errors.New("an upload with this id is in progress")
	ErrNotFound      = store.ErrNotFound
	ErrStillLinked   = errors.New("attachment is linked to a note")
	ErrAlreadyLinked = errors.New("attachment is already used")
)

// Config holds the limits (CORE-A3, docs/design/08 section 3).
type Config struct {
	MaxSize      int64 // per attachment; 25 MiB by default
	DefaultQuota int64 // per user, when none is set; 2 GiB by default
	ChunkSize    int   // 256 KiB
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{MaxSize: 25 << 20, DefaultQuota: 2 << 30, ChunkSize: 256 << 10}
}

// Service stores and serves attachments.
type Service struct {
	St  *store.Store
	Cfg Config
	Now func() time.Time
}

// New creates the service.
func New(st *store.Store, cfg Config) *Service {
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 256 << 10
	}
	return &Service{St: st, Cfg: cfg, Now: time.Now}
}

// Attachment is a stored file.
type Attachment struct {
	ID        uuid.UUID
	Filename  string
	MediaType string
	Size      int64
	SHA256    []byte
}

var mediaTypeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]{0,126}/[a-z0-9][a-z0-9!#$&^_.+-]{0,126}$`)

// CleanMediaType returns the media type without parameters, or application/octet-stream when it
// is not a well-formed type/subtype (the type is only ever used for display decisions, SEC-CNT-3).
func CleanMediaType(v string) string {
	base, _, _ := strings.Cut(v, ";")
	base = strings.ToLower(strings.TrimSpace(base))
	if mediaTypeRE.MatchString(base) {
		return base
	}
	return "application/octet-stream"
}

// CleanFilename makes a name safe to store and to put in a header: no control characters, no path
// separators, no leading dots, at most 255 bytes (SEC-CNT-4). The name is only ever a label;
// storage never uses it as a path.
func CleanFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == 0 || unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) || unicode.Is(unicode.Bidi_Control, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimLeft(strings.TrimSpace(b.String()), ".")
	for len(out) > 255 {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	if out == "" {
		return "file"
	}
	return out
}

// Upload streams body into storage as the attachment id (idempotent on id). declared is the
// Content-Length: the quota is reserved against it first, atomically, so two concurrent uploads
// can never both pass a check the other has already used up (SEC-CNT-6). The attachment is
// invisible until a note part references it.
func (s *Service) Upload(ctx context.Context, user, id uuid.UUID, filename, mediaType string, declared int64, body io.Reader) (Attachment, error) {
	switch {
	case declared <= 0:
		return Attachment{}, ErrEmpty
	case declared > s.Cfg.MaxSize:
		return Attachment{}, ErrTooLarge
	}
	filename, mediaType = CleanFilename(filename), CleanMediaType(mediaType)

	// The blob has its own id; the attachment keeps the client's id, which is unique per user only,
	// so an id that belongs to someone else behaves exactly like an unused one (SEC-ISO-3).
	blobID, err := uuid.NewV7()
	if err != nil {
		return Attachment{}, err
	}

	// Step 1 (locked): idempotency check, quota reservation, blob row.
	var existing *Attachment
	err = s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		if a, err := tx.Q.GetAttachmentWithBlob(ctx, dbq.GetAttachmentWithBlobParams{ID: id, UserID: user}); err == nil {
			existing = &Attachment{ID: a.ID, Filename: a.Filename, MediaType: a.MediaType, Size: a.SizeBytes, SHA256: a.Sha256}
			return nil
		}
		if running, err := tx.Q.UploadInProgress(ctx, dbq.UploadInProgressParams{UserID: user, UploadID: uuid.NullUUID{UUID: id, Valid: true}}); err != nil {
			return err
		} else if running {
			return ErrUploadRunning // an earlier attempt is still being cleaned up or is in flight
		}
		if err := tx.Q.EnsureStorageRow(ctx, user); err != nil {
			return err
		}
		n, err := tx.Q.ReserveQuota(ctx, dbq.ReserveQuotaParams{N: declared, UserID: user, DefaultQuota: s.Cfg.DefaultQuota})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrQuota
		}
		if err := tx.Q.InsertBlob(ctx, dbq.InsertBlobParams{ID: blobID, UserID: user, SizeBytes: declared, ChunkSize: int32(s.Cfg.ChunkSize),
			UploadID: uuid.NullUUID{UUID: id, Valid: true}}); err != nil {
			if store.IsUniqueViolation(err, "") {
				return ErrUploadRunning
			}
			return err
		}
		return nil
	})
	if err != nil {
		return Attachment{}, err
	}
	if existing != nil {
		return *existing, nil
	}

	// Step 2 (unlocked): stream the chunks, one short transaction each.
	sum := sha256.New()
	buf := make([]byte, s.Cfg.ChunkSize)
	src := io.LimitReader(body, declared+1) // one byte more than declared reveals an overrun
	var total int64
	for idx := int32(0); ; idx++ {
		n, rerr := io.ReadFull(src, buf)
		if n > 0 {
			total += int64(n)
			if total > declared {
				s.abort(user, blobID, declared)
				return Attachment{}, ErrLength
			}
			sum.Write(buf[:n])
			chunk := buf[:n]
			if err := s.St.InUserScoped(ctx, user, func(q *dbq.Queries) error {
				return q.InsertChunk(ctx, dbq.InsertChunkParams{UserID: user, BlobID: blobID, Idx: idx, Data: chunk})
			}); err != nil {
				s.abort(user, blobID, declared)
				return Attachment{}, err
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			s.abort(user, blobID, declared)
			return Attachment{}, rerr
		}
	}
	if total != declared {
		s.abort(user, blobID, declared)
		return Attachment{}, ErrLength
	}

	// Step 3 (locked): publish. Identical content is stored once per user (CORE-A5, SEC-ISO-9).
	digest := sum.Sum(nil)
	var out Attachment
	err = s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		blob := blobID
		stored := declared
		if dup, err := tx.Q.FindDuplicateBlob(ctx, dbq.FindDuplicateBlobParams{UserID: user, Sha256: digest, SizeBytes: declared, ID: blobID}); err == nil {
			if _, err := tx.Q.DeleteBlob(ctx, dbq.DeleteBlobParams{ID: blobID, UserID: user}); err != nil { // its chunks cascade
				return err
			}
			blob, stored = dup, 0
		} else if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.Q.CompleteBlob(ctx, dbq.CompleteBlobParams{ID: blobID, UserID: user, SizeBytes: declared, Sha256: digest}); err != nil {
				return err
			}
		} else {
			return err
		}
		if err := tx.Q.CommitReservation(ctx, dbq.CommitReservationParams{N: declared, Stored: stored, UserID: user}); err != nil {
			return err
		}
		a, err := tx.Q.InsertAttachment(ctx, dbq.InsertAttachmentParams{ID: id, UserID: user, BlobID: blob, Filename: filename, MediaType: mediaType})
		if err != nil {
			return err
		}
		out = Attachment{ID: a.ID, Filename: a.Filename, MediaType: a.MediaType, Size: declared, SHA256: digest}
		return nil
	})
	if err != nil {
		s.abort(user, blobID, declared)
		return Attachment{}, err
	}
	return out, nil
}

// abort releases the reservation and deletes the incomplete blob. It must succeed even when the
// request context is already cancelled (a dropped connection), so it has its own deadline.
func (s *Service) abort(user, id uuid.UUID, declared int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		b, err := tx.Q.GetBlob(ctx, dbq.GetBlobParams{ID: id, UserID: user})
		if err != nil || b.Complete {
			return nil // nothing to undo, or the upload actually finished
		}
		if _, err := tx.Q.DeleteBlob(ctx, dbq.DeleteBlobParams{ID: id, UserID: user}); err != nil {
			return err
		}
		return tx.Q.ReleaseReservation(ctx, dbq.ReleaseReservationParams{N: declared, UserID: user})
	})
}

// Info describes attachments by id, for rendering note parts.
func (s *Service) Info(ctx context.Context, q *dbq.Queries, user uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]Attachment, error) {
	out := map[uuid.UUID]Attachment{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.PartAttachmentInfo(ctx, dbq.PartAttachmentInfoParams{Column1: ids, UserID: user})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = Attachment{ID: r.ID, Filename: r.Filename, MediaType: r.MediaType, Size: r.SizeBytes}
	}
	return out, nil
}

// Delete removes an attachment inside the caller's transaction. Its blob goes too when no other
// attachment shares it, and the space is returned to the quota, so nothing is orphaned (CORE-A8).
func (s *Service) Delete(ctx context.Context, tx *store.UserTx, id uuid.UUID) error {
	a, err := tx.Q.GetAttachment(ctx, dbq.GetAttachmentParams{ID: id, UserID: tx.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	blob, err := tx.Q.GetBlob(ctx, dbq.GetBlobParams{ID: a.BlobID, UserID: tx.UserID})
	if err != nil {
		return err
	}
	if err := tx.Q.DeleteAttachment(ctx, dbq.DeleteAttachmentParams{ID: id, UserID: tx.UserID}); err != nil {
		return err
	}
	shared, err := tx.Q.BlobIsShared(ctx, dbq.BlobIsSharedParams{BlobID: a.BlobID, UserID: tx.UserID})
	if err != nil || shared {
		return err
	}
	if _, err := tx.Q.DeleteBlob(ctx, dbq.DeleteBlobParams{ID: a.BlobID, UserID: tx.UserID}); err != nil {
		return err
	}
	return tx.Q.ReleaseUsed(ctx, dbq.ReleaseUsedParams{N: blob.SizeBytes, UserID: tx.UserID})
}

// ReleaseStale cleans up after uploads that never finished and attachments never used by a note,
// both older than the given age (janitor, docs/design/01-data-model.md section 6.2).
func (s *Service) ReleaseStale(ctx context.Context, user uuid.UUID, olderThan time.Duration) error {
	cutoff := s.Now().Add(-olderThan)
	return s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		blobs, err := tx.Q.StaleIncompleteBlobs(ctx, dbq.StaleIncompleteBlobsParams{UserID: user, CreatedAt: cutoff})
		if err != nil {
			return err
		}
		for _, b := range blobs {
			if _, err := tx.Q.DeleteBlob(ctx, dbq.DeleteBlobParams{ID: b.ID, UserID: user}); err != nil {
				return err
			}
			if err := tx.Q.ReleaseReservation(ctx, dbq.ReleaseReservationParams{N: b.ReservedBytes, UserID: user}); err != nil {
				return err
			}
		}
		unlinked, err := tx.Q.UnlinkedAttachments(ctx, dbq.UnlinkedAttachmentsParams{UserID: user, CreatedAt: cutoff})
		if err != nil {
			return err
		}
		for _, id := range unlinked {
			if err := s.Delete(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- reading ---------------------------------------------------------------------------------

// Open returns a seekable reader over an attachment. Only one chunk is held in memory, so a
// download costs the same whatever the file size (CORE-A6, CORE-A8, NFR-API4). Seek never reads.
func (s *Service) Open(ctx context.Context, user, id uuid.UUID) (*Reader, Attachment, error) {
	var a Attachment
	var r *Reader
	err := s.St.InUserRead(ctx, user, func(q *dbq.Queries) error {
		row, err := q.GetAttachmentWithBlob(ctx, dbq.GetAttachmentWithBlobParams{ID: id, UserID: user})
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		a = Attachment{ID: row.ID, Filename: row.Filename, MediaType: row.MediaType, Size: row.SizeBytes, SHA256: row.Sha256}
		r = &Reader{ctx: ctx, s: s, user: user, blob: row.BlobID, size: row.SizeBytes, chunk: int64(row.ChunkSize), curIdx: -1}
		return nil
	})
	return r, a, err
}

// Reader is an io.ReadSeeker over a stored blob.
type Reader struct {
	ctx   context.Context
	s     *Service
	user  uuid.UUID
	blob  uuid.UUID
	size  int64
	chunk int64
	off   int64

	cur    []byte
	curIdx int64
}

func (r *Reader) Read(p []byte) (int, error) {
	if r.off >= r.size {
		return 0, io.EOF
	}
	idx := r.off / r.chunk
	if idx != r.curIdx {
		var data []byte
		err := r.s.St.InUserRead(r.ctx, r.user, func(q *dbq.Queries) error {
			var err error
			data, err = q.GetChunk(r.ctx, dbq.GetChunkParams{BlobID: r.blob, Idx: int32(idx)})
			return err
		})
		if err != nil {
			return 0, fmt.Errorf("read chunk %d: %w", idx, err)
		}
		r.cur, r.curIdx = data, idx
	}
	n := copy(p, r.cur[r.off-idx*r.chunk:])
	r.off += int64(n)
	return n, nil
}

// Seek implements io.Seeker without reading anything.
func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.off + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, errors.New("blobs: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("blobs: negative position")
	}
	r.off = abs
	return abs, nil
}
