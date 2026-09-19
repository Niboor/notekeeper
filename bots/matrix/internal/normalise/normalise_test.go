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
