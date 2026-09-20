package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Niboor/notekeeper/core/internal/blobs"
	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/Niboor/notekeeper/core/internal/httpx"
)

// uploadDeadline bounds a single upload (docs/design/02-api.md section 4).
const uploadDeadline = 10 * time.Minute

// UploadIdleTimeout is how long an upload may go without receiving a byte before it is dropped, so a
// client that sends a little and then stalls cannot hold an upload slot and a quota reservation for the
// whole deadline (SR-017). Set from NK_UPLOAD_IDLE_TIMEOUT.
var UploadIdleTimeout = 60 * time.Second

// idleBody wraps a request body so that every read must be served within UploadIdleTimeout. The context
// deadline alone cannot interrupt a read that is blocked on a stalled connection; a read deadline can.
func idleBody(w http.ResponseWriter, body io.Reader) io.Reader {
	if w == nil || UploadIdleTimeout <= 0 {
		return body
	}
	rc := http.NewResponseController(w)
	return readerFunc(func(p []byte) (int, error) {
		_ = rc.SetReadDeadline(time.Now().Add(UploadIdleTimeout)) // not supported on every writer: then only the context bounds it
		return body.Read(p)
	})
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func (u *userAPI) UploadAttachment(ctx context.Context, req userapi.UploadAttachmentRequestObject) (userapi.UploadAttachmentResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	w, r := httpx.HTTPFrom(ctx)
	if r.ContentLength < 0 {
		return nil, httpx.NewError(http.StatusLengthRequired, "length_required")
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	name, err := url.PathUnescape(req.Params.XFilename)
	if err != nil {
		return nil, errBadRequest.WithDetail("X-Filename is not valid percent-encoding")
	}
	mediaType := ""
	if req.Params.XMediaType != nil {
		mediaType = *req.Params.XMediaType
	}
	ctx, cancel := context.WithTimeout(ctx, uploadDeadline)
	defer cancel()
	a, err := u.blobs.Upload(ctx, p.UserID, req.Id, name, mediaType, r.ContentLength, idleBody(w, req.Body))
	if err != nil {
		return nil, mapBlobError(err)
	}
	return userapi.UploadAttachment201JSONResponse{Id: a.ID, Filename: a.Filename, MediaType: a.MediaType, Size: a.Size}, nil
}

func mapBlobError(err error) error {
	switch {
	case errors.Is(err, blobs.ErrQuota):
		return httpx.NewError(http.StatusRequestEntityTooLarge, "quota_exceeded")
	case errors.Is(err, blobs.ErrTooLarge):
		return httpx.NewError(http.StatusRequestEntityTooLarge, "too_large")
	case errors.Is(err, blobs.ErrEmpty), errors.Is(err, blobs.ErrLength):
		return httpx.NewError(http.StatusBadRequest, "invalid_attachment").WithDetail(err.Error())
	case errors.Is(err, blobs.ErrUploadRunning):
		return httpx.NewError(http.StatusConflict, "upload_in_progress")
	}
	return err
}

// inlineTypes may be displayed by the browser; everything else is served as a download of
// application/octet-stream, so an uploaded file can never run as a page (SEC-CNT-3). SVG is
// deliberately absent: it can carry script.
var inlineTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/avif": true,
	"application/pdf": true,
	"audio/mpeg":      true, "audio/ogg": true, "audio/wav": true, "audio/webm": true, "audio/mp4": true, "audio/flac": true, "audio/aac": true,
	"video/mp4": true, "video/webm": true, "video/ogg": true,
}

// attachmentDownload streams a file with ServeContent, which handles Range, If-None-Match and
// If-Modified-Since. Memory use is one chunk (blobs.Reader).
type attachmentDownload struct {
	r      *http.Request
	reader *blobs.Reader
	att    blobs.Attachment
	cache  string // Cache-Control; the default is for the signed-in owner
}

func (d attachmentDownload) VisitDownloadAttachmentResponse(w http.ResponseWriter) error {
	return d.serve(w)
}

func (d attachmentDownload) serve(w http.ResponseWriter) error {
	h := w.Header()
	disposition, contentType := "attachment", "application/octet-stream"
	if inlineTypes[d.att.MediaType] {
		disposition, contentType = "inline", d.att.MediaType
	}
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", contentDisposition(disposition, d.att.Filename))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	if d.cache == "" {
		d.cache = "private, no-cache" // design decision D5
	}
	h.Set("Cache-Control", d.cache)
	if len(d.att.SHA256) > 0 {
		h.Set("ETag", fmt.Sprintf(`"%x"`, d.att.SHA256[:16]))
	}
	// A request for several ranges makes Go read and send each one, and a few kilobytes of header can
	// ask for hundreds of overlapping pieces, each of which costs a database read. Browsers and media
	// players ask for one range at a time, so more than one is refused (SR-009, SEC-SHR-8).
	if strings.Contains(d.r.Header.Get("Range"), ",") {
		h.Set("Content-Range", fmt.Sprintf("bytes */%d", d.att.Size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return nil
	}
	http.ServeContent(w, d.r, "", time.Time{}, d.reader)
	return nil
}

// contentDisposition builds a header value with an ASCII fallback and an RFC 5987 UTF-8 name.
// Filenames were already cleaned of control characters at upload (SEC-CNT-4); this encodes the rest.
func contentDisposition(kind, name string) string {
	var ascii strings.Builder
	for _, r := range name {
		if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._- ", r)) {
			ascii.WriteRune(r)
		} else {
			ascii.WriteRune('_')
		}
	}
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, kind, ascii.String(), url.PathEscape(name))
}

func (u *userAPI) DownloadAttachment(ctx context.Context, req userapi.DownloadAttachmentRequestObject) (userapi.DownloadAttachmentResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	reader, att, err := u.blobs.Open(ctx, p.UserID, req.Id)
	if err != nil {
		return nil, err
	}
	_, r := httpx.HTTPFrom(ctx)
	return attachmentDownload{r: r, reader: reader, att: att}, nil
}
