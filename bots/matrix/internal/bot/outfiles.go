package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/Niboor/notekeeper/bots/sdk/botclient"
)

type outFile struct {
	ID        uuid.UUID
	Filename  string
	MediaType string
	Size      int64
}

func filesOf(it botclient.OutboxItem) []outFile {
	raw, _ := it.Payload["attachments"].([]interface{})
	var out []outFile
	for i, r := range raw {
		m, _ := r.(map[string]interface{})
		id, err := uuid.Parse(fmt.Sprint(m["id"]))
		if err != nil || i >= 10 { // a reminder sends a handful of files at most
			continue
		}
		size, _ := m["size"].(float64)
		out = append(out, outFile{ID: id, Filename: fmt.Sprint(m["filename"]), MediaType: fmt.Sprint(m["media_type"]), Size: int64(size)})
	}
	return out
}

// sendFiles re-sends the files of a reminder into the chat as Matrix media, encrypted when the room
// is (MX-12, CORE-R6). A file that cannot be sent is replaced by a line naming it with the link to the
// note, so a failed file never blocks or hides the reminder itself.
func (b *Bot) sendFiles(ctx context.Context, room id.RoomID, it botclient.OutboxItem) {
	link, _ := it.Payload["note_url"].(string)
	for i, f := range filesOf(it) {
		txn := fmt.Sprintf("%s-f%d", it.Id, i)
		if err := b.sendFile(ctx, room, it, f, txn); err != nil {
			b.met.attachments.WithLabelValues("send_failed").Inc()
			b.log.Warn("could not send a reminder file", "room", room, "item", it.Id, "error", err)
			note := "📎 " + f.Filename + " could not be sent here."
			if link != "" {
				note += " Open the note: " + link
			}
			if _, err := b.client.SendMessageEvent(ctx, room, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgNotice, Body: note},
				mautrix.ReqSendEvent{TransactionID: txn + "-n"}); err != nil {
				b.log.Warn("could not send the fallback line", "room", room, "error", err)
			}
		}
	}
}

func (b *Bot) sendFile(ctx context.Context, room id.RoomID, it botclient.OutboxItem, f outFile, txn string) error {
	if f.Size <= 0 || f.Size > b.cfg.MaxAttachmentBytes {
		return errors.New("file size not acceptable")
	}
	body, length, err := b.core.OutboxAttachment(ctx, it.Id, f.ID)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	if length != f.Size {
		return errors.New("file size differs from what the reminder listed")
	}
	encrypted := false
	if b.client.StateStore != nil {
		encrypted, _ = b.client.StateStore.IsEncrypted(ctx, room)
	}
	mediaType := f.MediaType
	content := &event.MessageEventContent{MsgType: msgTypeFor(mediaType), Body: f.Filename, FileName: f.Filename, Info: &event.FileInfo{MimeType: mediaType, Size: int(f.Size)}}
	var upload *mautrix.RespMediaUpload
	if encrypted {
		enc := attachment.NewEncryptedFile()
		stream := enc.EncryptStream(body)
		upload, err = b.client.UploadMedia(ctx, mautrix.ReqUploadMedia{Content: struct{ io.Reader }{stream}, ContentLength: f.Size, ContentType: "application/octet-stream", FileName: f.Filename})
		if cerr := stream.Close(); err == nil { // Close finalises the checksum the recipient verifies
			err = cerr
		}
		if err != nil {
			return err
		}
		content.File = &event.EncryptedFileInfo{EncryptedFile: *enc, URL: upload.ContentURI.CUString()}
	} else {
		upload, err = b.client.UploadMedia(ctx, mautrix.ReqUploadMedia{Content: body, ContentLength: f.Size, ContentType: mediaType, FileName: f.Filename})
		if err != nil {
			return err
		}
		content.URL = upload.ContentURI.CUString()
	}
	_, err = b.client.SendMessageEvent(ctx, room, event.EventMessage, content, mautrix.ReqSendEvent{TransactionID: txn})
	return err
}

func msgTypeFor(mediaType string) event.MessageType {
	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return event.MsgImage
	case strings.HasPrefix(mediaType, "audio/"):
		return event.MsgAudio
	case strings.HasPrefix(mediaType, "video/"):
		return event.MsgVideo
	}
	return event.MsgFile
}
