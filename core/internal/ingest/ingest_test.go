package ingest

import (
	"errors"
	"testing"
	"time"
)

// A message with a NUL byte or broken UTF-8 must be stored (repaired), never turn into a 500 that a
// bot would retry for ever (CR-003, BOT-7).
func TestChatTextIsRepairedNotRefused(t *testing.T) {
	ev := Event{
		EventID: "$1", Sender: "@a:x", Conversation: "!r:x", MessageID: "$1", Timestamp: time.Now(), Kind: KindCreated,
		Parts: []Part{
			{Type: PartText, Text: "he\x00llo \xff\xfe world"},
			{Type: PartAttachmentFailed, Filename: "a\x00b.png", Reason: "too_large"},
		},
	}
	ev.sanitize()
	if err := ev.validate(); err != nil {
		t.Fatalf("a repairable message was refused: %v", err)
	}
	if got := ev.Parts[0].Text; got != "hello � world" {
		t.Fatalf("text = %q", got)
	}
	if got := ev.Parts[1].Filename; got != "ab.png" {
		t.Fatalf("filename = %q", got)
	}
}

func TestOnlyTextIsRepairedIdentifiersAreRefused(t *testing.T) {
	ev := Event{EventID: "$1\x00", Sender: "@a:x", Conversation: "!r:x", MessageID: "$1", Timestamp: time.Now(), Kind: KindCreated,
		Parts: []Part{{Type: PartText, Text: "hi"}}}
	if err := ev.validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestCleanTextLeavesGoodTextAlone(t *testing.T) {
	for _, s := range []string{"", "plain", "emoji 🙂 and ünïcödé", "line\nbreak\ttab"} {
		if got := cleanText(s); got != s {
			t.Errorf("cleanText(%q) = %q", s, got)
		}
	}
}
