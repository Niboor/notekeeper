// Package normalise turns Matrix events into bot API events and commands (docs/design/06-matrix-bot.md
// section 5). It is pure: no network, no state. The bot contains platform logic only; what an
// event means (grouping, edits, wording) is decided by Core (BOT-B1).
package normalise

import (
	"strings"
	"time"

	"maunium.net/go/mautrix/event"

	"github.com/Niboor/notekeeper/bots/sdk/botclient"
)

// Commands the bot recognises. A message is a command only when its body starts with `!` and one
// of these names; anything else that starts with `!` is an ordinary note (MX-8, SEC-BOT-12).
var Commands = map[string]bool{"link": true, "unlink": true, "help": true, "remind": true, "snooze": true, "done": true}

// Kind says what a Matrix event turned into.
type Kind int

// Kinds of result.
const (
	KindIgnore  Kind = iota // not for Core (own notices, reactions, unknown event types, empty messages)
	KindEvent               // a message to save, edit or delete: Result.Event
	KindCommand             // a chat command: Result.Command
)

// Result is the outcome of normalising one Matrix event.
type Result struct {
	Kind    Kind
	Event   *botclient.Event
	Command *botclient.Command
}

// Message normalises a decrypted m.room.message event.
func Message(evt *event.Event) Result {
	content := evt.Content.AsMessage()
	if content == nil || content.MsgType == event.MsgNotice {
		return Result{} // notices are the bot's own voice (and other bots'): never notes (MX-4)
	}
	ts := time.UnixMilli(evt.Timestamp).UTC()
	rel := content.OptionalGetRelatesTo()

	// An edit replaces an earlier message; the new text lives in m.new_content (EDT-1).
	if rel != nil && rel.GetReplaceID() != "" {
		nc := content.NewContent
		if nc == nil {
			return Result{}
		}
		parts := partsOf(nc, "")
		if len(parts) == 0 {
			return Result{}
		}
		return Result{Kind: KindEvent, Event: &botclient.Event{
			EventId: evt.ID.String(), Kind: botclient.MessageEdited, Sender: evt.Sender.String(),
			Conversation: evt.RoomID.String(), MessageId: rel.GetReplaceID().String(), Timestamp: ts, Parts: &parts,
		}}
	}

	replyTo, thread := "", ""
	if rel != nil {
		replyTo, thread = rel.GetReplyTo().String(), rel.GetThreadParent().String()
	}
	body := content.Body
	if replyTo != "" {
		body = StripReplyFallback(body) // legacy clients quote the replied-to text into the body
	}

	if content.MsgType == event.MsgText || content.MsgType == event.MsgEmote {
		if name, args, ok := ParseCommand(body); ok {
			var replyID *string
			if replyTo != "" {
				replyID = &replyTo
			}
			id := evt.ID.String()
			return Result{Kind: KindCommand, Command: &botclient.Command{
				Command: name, Args: &args, Sender: evt.Sender.String(), Conversation: evt.RoomID.String(),
				MessageId: &id, ReplyTo: replyID, Timestamp: &ts,
			}}
		}
	}

	parts := partsOf(content, body)
	if len(parts) == 0 {
		return Result{}
	}
	e := &botclient.Event{
		EventId: evt.ID.String(), Kind: botclient.MessageCreated, Sender: evt.Sender.String(),
		Conversation: evt.RoomID.String(), MessageId: evt.ID.String(), Timestamp: ts, Parts: &parts,
	}
	if replyTo != "" || thread != "" {
		e.RelatesTo = &struct {
			ReplyTo *string `json:"reply_to,omitempty"`
			Thread  *string `json:"thread,omitempty"`
		}{}
		if replyTo != "" {
			e.RelatesTo.ReplyTo = &replyTo
		}
		if thread != "" {
			e.RelatesTo.Thread = &thread
		}
	}
	return Result{Kind: KindEvent, Event: e}
}

// Redaction normalises a redaction (a deleted message) into a message_deleted event (EDT-4).
func Redaction(evt *event.Event) Result {
	if evt.Redacts == "" {
		return Result{}
	}
	return Result{Kind: KindEvent, Event: &botclient.Event{
		EventId: evt.ID.String(), Kind: botclient.MessageDeleted, Sender: evt.Sender.String(),
		Conversation: evt.RoomID.String(), MessageId: evt.Redacts.String(), Timestamp: time.UnixMilli(evt.Timestamp).UTC(),
	}}
}

func partsOf(content *event.MessageEventContent, body string) []botclient.EventPart {
	if body == "" {
		body = content.Body
	}
	text := func(s string) botclient.EventPart {
		return botclient.EventPart{Type: botclient.Text, Text: &s}
	}
	switch content.MsgType {
	case event.MsgText, event.MsgEmote, "":
		if strings.TrimSpace(body) == "" {
			return nil
		}
		return []botclient.EventPart{text(strings.TrimRight(body, " \t\r\n"))}
	case event.MsgLocation:
		s := strings.TrimSpace(body)
		if content.GeoURI != "" {
			s = strings.TrimSpace(s + " " + content.GeoURI)
		}
		if s == "" {
			return nil
		}
		return []botclient.EventPart{text(s)}
	default:
		// Attachments arrive with milestone M3; until then nothing is dropped silently (MX-5).
		desc := unsupportedDescription(content)
		return []botclient.EventPart{{Type: botclient.Unsupported, Description: &desc}}
	}
}

func unsupportedDescription(c *event.MessageEventContent) string {
	kind := "Unsupported message"
	switch c.MsgType {
	case event.MsgImage:
		kind = "Image"
	case event.MsgFile:
		kind = "File"
	case event.MsgAudio:
		kind = "Audio"
	case event.MsgVideo:
		kind = "Video"
	}
	if name := c.GetFileName(); name != "" {
		return kind + ": " + name
	}
	return kind
}

// ParseCommand recognises `!name args` for known command names only.
func ParseCommand(body string) (name, args string, ok bool) {
	body = strings.TrimSpace(body)
	rest, found := strings.CutPrefix(body, "!")
	if !found || rest == "" {
		return "", "", false
	}
	// The name ends at the first whitespace (space, tab or newline): "!link\nCODE" is still `link`.
	end := strings.IndexAny(rest, " \t\r\n")
	first, after := rest, ""
	if end >= 0 {
		first, after = rest[:end], strings.TrimSpace(rest[end:])
	}
	first = strings.ToLower(first)
	if !Commands[first] {
		return "", "", false
	}
	return first, after, true
}

// StripReplyFallback removes the quoted block older clients put before the actual text of a reply:
// leading lines that start with "> ", followed by one blank line.
func StripReplyFallback(body string) string {
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) && strings.HasPrefix(lines[i], ">") {
		i++
	}
	if i == 0 {
		return body
	}
	if i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	return strings.Join(lines[i:], "\n")
}
