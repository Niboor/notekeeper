package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/Niboor/notekeeper/bots/matrix/internal/normalise"
	"github.com/Niboor/notekeeper/bots/sdk"
	"github.com/Niboor/notekeeper/bots/sdk/botclient"
)

// Reasons recorded for an attachment that could not be saved. Core stores them; the app words them.
const (
	reasonTooLarge   = "too_large"
	reasonDownload   = "download_failed"
	reasonCorrupt    = "corrupt"
	reasonEncryption = "unsupported_encryption"
	reasonUpload     = "upload_failed"
	reasonQuota      = "quota_exceeded"
	reasonNoLength   = "size_unknown"
)

var uploadNamespace = uuid.NewSHA1(uuid.NameSpaceDNS, []byte("notekeeper-matrix-upload"))

// uploadID is derived from the Matrix event, so a retry or a replay of the same message uploads to
// the same id instead of piling up orphans.
func uploadID(evt id.EventID, part int) uuid.UUID {
	return uuid.NewSHA1(uploadNamespace, fmt.Appendf(nil, "%s/%d", evt, part))
}

// fetchMedia downloads each file of the event and uploads it to Core, streaming with no buffer in
// between (BOT-6, NFR-P3). A file that cannot be moved becomes an attachment_failed part with a
// reason, so the rest of the message is never lost (MX-5, CORE-A9).
func (b *Bot) fetchMedia(ctx context.Context, evt *event.Event, res *normalise.Result) {
	if res.Event == nil || res.Event.Parts == nil {
		return
	}
	parts := *res.Event.Parts
	for _, m := range res.Media {
		uid := uploadID(evt.ID, m.Part)
		reason := b.moveFile(ctx, evt, m, uid)
		if reason == "" {
			parts[m.Part].UploadId = &uid
			b.met.attachments.WithLabelValues("ok").Inc()
			continue
		}
		b.met.attachments.WithLabelValues(reason).Inc()
		b.log.Warn("attachment could not be saved", "room", evt.RoomID, "event", evt.ID, "reason", reason)
		p := &parts[m.Part]
		p.Type, p.Reason = botclient.AttachmentFailed, &reason
	}
}

// moveFile returns "" on success, otherwise the reason it failed.
func (b *Bot) moveFile(ctx context.Context, evt *event.Event, m normalise.Media, uid uuid.UUID) string {
	if m.Size > b.cfg.MaxAttachmentBytes {
		return reasonTooLarge // refused before downloading a single byte
	}
	uri, err := m.URL.Parse()
	if err != nil {
		return reasonDownload
	}
	if m.File != nil {
		if err := m.File.PrepareForDecryption(); err != nil {
			return reasonEncryption
		}
	}
	err = b.core.Upload(ctx, uid, evt.Sender.String(), m.Filename, m.MediaType, func() (sdk.Body, error) {
		resp, err := b.client.Download(ctx, uri)
		if err != nil {
			var httpErr mautrix.HTTPError
			if errors.As(err, &httpErr) && httpErr.Response != nil && httpErr.Response.StatusCode >= 400 && httpErr.Response.StatusCode < 500 &&
				httpErr.Response.StatusCode != http.StatusTooManyRequests {
				return sdk.Body{}, &sdk.PermanentError{Status: httpErr.Response.StatusCode, Code: reasonDownload} // gone, or not ours to read
			}
			return sdk.Body{}, err // network trouble or a 5xx: worth another attempt
		}
		fail := func(e error) (sdk.Body, error) { _ = resp.Body.Close(); return sdk.Body{}, e }
		switch {
		case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests:
			return fail(&sdk.PermanentError{Status: resp.StatusCode, Code: reasonDownload})
		case resp.StatusCode != http.StatusOK:
			return fail(fmt.Errorf("homeserver answered %d", resp.StatusCode))
		case resp.ContentLength < 0:
			return fail(&sdk.PermanentError{Code: reasonNoLength}) // the request needs a length up front
		case resp.ContentLength > b.cfg.MaxAttachmentBytes:
			return fail(&sdk.PermanentError{Code: reasonTooLarge}) // the sender's size claim was wrong
		case resp.ContentLength == 0:
			return fail(&sdk.PermanentError{Code: reasonDownload})
		}
		if m.File == nil {
			return sdk.Body{Reader: resp.Body, Size: resp.ContentLength, Finish: resp.Body.Close}, nil
		}
		// The checksum of an encrypted file is only known at the end of the stream, and Close reports it.
		// The stream must not be handed to the HTTP client as a closer, which would swallow that error.
		stream := m.File.DecryptStream(resp.Body)
		return sdk.Body{Reader: struct{ io.Reader }{stream}, Size: resp.ContentLength, Finish: stream.Close}, nil
	})
	return classify(err)
}

func classify(err error) string {
	if err == nil {
		return ""
	}
	var perm *sdk.PermanentError
	switch {
	case errors.As(err, &perm):
		switch perm.Code {
		case reasonTooLarge, reasonNoLength, reasonDownload:
			return perm.Code
		case "quota_exceeded":
			return reasonQuota
		}
	case errors.Is(err, attachment.ErrHashMismatch):
		return reasonCorrupt
	}
	return reasonUpload
}
