package normalise

import (
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func msg(c *event.MessageEventContent) *event.Event {
	return &event.Event{
		ID: "$evt", RoomID: "!room:example.org", Sender: "@robin:example.org", Timestamp: 1_800_000_000_123,
		Type: event.EventMessage, Content: event.Content{Parsed: c},
	}
}

func textOf(t *testing.T, r Result) string {
	t.Helper()
	if r.Kind != KindEvent || r.Event == nil || r.Event.Parts == nil || len(*r.Event.Parts) != 1 || (*r.Event.Parts)[0].Text == nil {
		t.Fatalf("not a single text part: %+v", r)
	}
	return *(*r.Event.Parts)[0].Text
}

func TestPlainTextBecomesAnEvent(t *testing.T) {
	r := Message(msg(&event.MessageEventContent{MsgType: event.MsgText, Body: "buy milk\n"}))
	if textOf(t, r) != "buy milk" {
		t.Fatal("trailing whitespace should be trimmed")
	}
	e := r.Event
	if e.EventId != "$evt" || e.MessageId != "$evt" || e.Sender != "@robin:example.org" || e.Conversation != "!room:example.org" {
		t.Fatalf("identifiers: %+v", e)
	}
	if e.Timestamp.UnixMilli() != 1_800_000_000_123 {
		t.Fatalf("platform timestamp must be kept with millisecond precision: %v", e.Timestamp)
	}
}

func TestMarkdownIsKeptAsTyped(t *testing.T) {
	// Element sends what the user typed in `body` and a rendering in formatted_body; notes keep the source.
	body := "- [ ] eggs\n- [x] milk\n**urgent**"
	r := Message(msg(&event.MessageEventContent{MsgType: event.MsgText, Body: body, Format: event.FormatHTML, FormattedBody: "<ul>…</ul>"}))
	if textOf(t, r) != body {
		t.Fatalf("markdown source must be preserved, got %q", textOf(t, r))
	}
}

func TestNoticesAndEmptyMessagesAreIgnored(t *testing.T) {
	for name, c := range map[string]*event.MessageEventContent{
		"notice": {MsgType: event.MsgNotice, Body: "I am a bot"},
		"empty":  {MsgType: event.MsgText, Body: " \n "},
	} {
		if r := Message(msg(c)); r.Kind != KindIgnore {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

// Chat text is a command only at the very start and only with a known name (SEC-BOT-12, MX-8).
func TestCommands(t *testing.T) {
	cases := []struct {
		body, name, args string
		ok               bool
	}{
		{"!link ABCD-1234", "link", "ABCD-1234", true},
		{"!LINK  abcd-1234  ", "link", "abcd-1234", true},
		{"!help", "help", "", true},
		{"!remind tomorrow 9am call dentist", "remind", "tomorrow 9am call dentist", true},
		{"!link\nABCD-1234", "link", "ABCD-1234", true},
		{"!important: buy milk", "", "", false}, // unknown name: an ordinary note (MX-8)
		{"! link", "", "", false},
		{"link ABCD", "", "", false},
		{"say !link now", "", "", false}, // only at the very start
		{"!", "", "", false},
	}
	for _, c := range cases {
		name, args, ok := ParseCommand(c.body)
		if ok != c.ok || name != c.name || args != c.args {
			t.Errorf("ParseCommand(%q) = %q, %q, %v; want %q, %q, %v", c.body, name, args, ok, c.name, c.args, c.ok)
		}
	}
	r := Message(msg(&event.MessageEventContent{MsgType: event.MsgText, Body: "!link ABCD-1234"}))
	if r.Kind != KindCommand || r.Command.Command != "link" || *r.Command.Args != "ABCD-1234" || r.Command.Sender != "@robin:example.org" {
		t.Fatalf("command: %+v", r)
	}
	// An unknown "!thing" message is saved as a note.
	if r := Message(msg(&event.MessageEventContent{MsgType: event.MsgText, Body: "!important call mum"})); r.Kind != KindEvent {
		t.Fatalf("unknown command word must be a note: %+v", r)
	}
}

func TestRepliesAndThreads(t *testing.T) {
	c := &event.MessageEventContent{
		MsgType:   event.MsgText,
		Body:      "> <@robin:example.org> concert friday\n\nalso bring cash",
		RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: "$earlier"}},
	}
	r := Message(msg(c))
	if textOf(t, r) != "also bring cash" {
		t.Fatalf("the quoted fallback must not become part of the note: %q", textOf(t, r))
	}
	if r.Event.RelatesTo == nil || *r.Event.RelatesTo.ReplyTo != "$earlier" {
		t.Fatalf("relates_to: %+v", r.Event.RelatesTo)
	}
	// A body that merely starts with ">" but is not a reply is left alone.
	if got := StripReplyFallback("> quote\n\ntext"); got != "text" {
		t.Fatalf("fallback: %q", got)
	}
	if r := Message(msg(&event.MessageEventContent{MsgType: event.MsgText, Body: "> just a quote"})); textOf(t, r) != "> just a quote" {
		t.Fatal("non-reply quote was modified")
	}
	thread := &event.MessageEventContent{MsgType: event.MsgText, Body: "in thread",
		RelatesTo: &event.RelatesTo{Type: event.RelThread, EventID: id.EventID("$root")}}
	if r := Message(msg(thread)); r.Event.RelatesTo == nil || *r.Event.RelatesTo.Thread != "$root" {
		t.Fatalf("thread: %+v", r.Event.RelatesTo)
	}
}

func TestEditsReferenceTheOriginalMessage(t *testing.T) {
	c := &event.MessageEventContent{
		MsgType: event.MsgText, Body: "* milk, eggs",
		NewContent: &event.MessageEventContent{MsgType: event.MsgText, Body: "milk, eggs"},
		RelatesTo:  &event.RelatesTo{Type: event.RelReplace, EventID: "$original"},
	}
	r := Message(msg(c))
	if r.Kind != KindEvent || string(r.Event.Kind) != "message_edited" || r.Event.MessageId != "$original" || r.Event.EventId != "$evt" {
		t.Fatalf("edit: %+v", r.Event)
	}
	if textOf(t, r) != "milk, eggs" {
		t.Fatal("an edit must carry the new content, without the '* ' fallback")
	}
}

func TestRedactionBecomesADelete(t *testing.T) {
	e := &event.Event{ID: "$red", RoomID: "!r:x", Sender: "@robin:x", Redacts: "$gone", Timestamp: 1}
	r := Redaction(e)
	if r.Kind != KindEvent || string(r.Event.Kind) != "message_deleted" || r.Event.MessageId != "$gone" || r.Event.Parts != nil {
		t.Fatalf("redaction: %+v", r.Event)
	}
	if Redaction(&event.Event{}).Kind != KindIgnore {
		t.Fatal("a redaction without a target is ignored")
	}
}

// (MX-4)
func TestOtherMessageTypesAreNeverDropped(t *testing.T) {
	r := Message(msg(&event.MessageEventContent{MsgType: event.MsgImage, Body: "cat.jpg", FileName: "cat.jpg"}))
	if r.Kind != KindEvent {
		t.Fatalf("image: %+v", r)
	}
	p := (*r.Event.Parts)[0]
	if string(p.Type) != "unsupported" || *p.Description != "Image: cat.jpg" {
		t.Fatalf("part: %+v", p)
	}
	loc := Message(msg(&event.MessageEventContent{MsgType: event.MsgLocation, Body: "Home", GeoURI: "geo:52.1,4.3"}))
	if textOf(t, loc) != "Home geo:52.1,4.3" {
		t.Fatalf("location: %q", textOf(t, loc))
	}
}

// Files become attachment parts with the caption as a following text part; where the bytes are is
// reported separately, and the sender's size claim is only carried, never trusted (MX-5, GRP-2).
func TestFilesBecomeAttachmentsWithCaption(t *testing.T) {
	c := &event.MessageEventContent{MsgType: event.MsgImage, Body: "Holiday at the beach", FileName: "beach.jpg",
		URL: "mxc://example.org/abc", Info: &event.FileInfo{MimeType: "image/jpeg", Size: 4096}}
	r := Message(msg(c))
	parts := *r.Event.Parts
	if len(parts) != 2 || string(parts[0].Type) != "attachment" || *parts[0].Filename != "beach.jpg" || *parts[0].MediaType != "image/jpeg" ||
		*parts[0].Size != 4096 || string(parts[1].Type) != "text" || *parts[1].Text != "Holiday at the beach" {
		t.Fatalf("parts: %+v", parts)
	}
	if len(r.Media) != 1 || r.Media[0].Part != 0 || r.Media[0].URL != "mxc://example.org/abc" || r.Media[0].File != nil {
		t.Fatalf("media: %+v", r.Media)
	}

	// Older clients put the file name in body: that is not a caption.
	old := Message(msg(&event.MessageEventContent{MsgType: event.MsgFile, Body: "report.pdf", URL: "mxc://example.org/x"}))
	if p := *old.Event.Parts; len(p) != 1 || *p[0].Filename != "report.pdf" {
		t.Fatalf("old-style file: %+v", p)
	}

	// Encrypted files carry their key material; the bot decrypts while streaming.
	enc := Message(msg(&event.MessageEventContent{MsgType: event.MsgVideo, Body: "clip.mp4", File: &event.EncryptedFileInfo{URL: "mxc://example.org/enc"}}))
	if len(enc.Media) != 1 || enc.Media[0].File == nil || enc.Media[0].URL != "mxc://example.org/enc" {
		t.Fatalf("encrypted: %+v", enc.Media)
	}

	// A file that cannot be fetched is still kept, as an unsupported placeholder.
	none := Message(msg(&event.MessageEventContent{MsgType: event.MsgImage, Body: "x.png"}))
	if len(none.Media) != 0 || string((*none.Event.Parts)[0].Type) != "unsupported" {
		t.Fatalf("no url: %+v", none)
	}
}

// Editing the caption of a file sends only the new caption; the file is not sent again (EDT-1).
// (MX-6)
func TestEditingACaption(t *testing.T) {
	c := &event.MessageEventContent{MsgType: event.MsgText, Body: "* new caption",
		RelatesTo:  &event.RelatesTo{Type: event.RelReplace, EventID: "$orig"},
		NewContent: &event.MessageEventContent{MsgType: event.MsgImage, Body: "new caption", FileName: "a.jpg", URL: "mxc://example.org/a"}}
	r := Message(msg(c))
	if r.Kind != KindEvent || string(r.Event.Kind) != "message_edited" || r.Event.MessageId != "$orig" || len(r.Media) != 0 {
		t.Fatalf("%+v", r)
	}
	if p := *r.Event.Parts; len(p) != 1 || string(p[0].Type) != "text" || *p[0].Text != "new caption" {
		t.Fatalf("parts: %+v", p)
	}
	noCaption := &event.MessageEventContent{MsgType: event.MsgText, Body: "* a.jpg",
		RelatesTo:  &event.RelatesTo{Type: event.RelReplace, EventID: "$orig"},
		NewContent: &event.MessageEventContent{MsgType: event.MsgImage, Body: "a.jpg", FileName: "a.jpg", URL: "mxc://example.org/a"}}
	if Message(msg(noCaption)).Kind != KindIgnore {
		t.Fatal("an edit that changes nothing textual is ignored")
	}
}
