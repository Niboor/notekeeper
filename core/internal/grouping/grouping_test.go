package grouping

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// The grouping policy is one pure function (GRP-9, NFR-Q1). Every scenario of docs/design/04-ingestion.md
// section 4 and every rule GRP-1..GRP-8 is a row here.

var (
	t0     = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	window = 60 * time.Second
	noteA  = uuid.MustParse("00000000-0000-7000-8000-00000000000a")
	partA  = uuid.MustParse("00000000-0000-7000-8000-0000000000a1")
)

func at(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }

var (
	textOnly  = Content{HasText: true}
	mediaOnly = Content{HasMedia: true}
	mixed     = Content{HasText: true, HasMedia: true}
)

// noteWith describes the sender's most recent active note in the conversation.
func noteWith(lastPart time.Time, hasText bool) *Candidate {
	return &Candidate{NoteID: noteA, LastPartAt: lastPart, HasText: hasText}
}

// Decide is one pure function, tested without any I/O (GRP-9, NFR-Q1).
func TestDecide(t *testing.T) {
	cases := []struct {
		name    string
		ev      Event
		cand    *Candidate
		rel     *Related
		want    Target
		reason  string
		relPart *uuid.UUID
	}{
		// ---- design 04 section 4 walk-through
		{"F2: the first message starts a note", Event{at(0), textOnly}, nil, nil, New, ReasonFirst, nil},
		{"F3: a PDF, then its caption 20 s later, is one note", Event{at(20), textOnly}, noteWith(at(0), false), nil, Existing, ReasonMedia, nil},
		{"F4: two quick text messages stay separate notes (GRP-4)", Event{at(5), textOnly}, noteWith(at(0), true), nil, New, ReasonFirst, nil},
		{"photo burst: media joins the note within the window (GRP-3)", Event{at(4), mediaOnly}, noteWith(at(0), false), nil, Existing, ReasonMedia, nil},
		{"a photo after a text message joins it, the text being its caption (GRP-2a)", Event{at(8), mediaOnly}, noteWith(at(0), true), nil, Existing, ReasonMedia, nil},
		{"text, file, another file, then text: the last text starts a new note", Event{at(20), textOnly}, noteWith(at(15), true), nil, New, ReasonFirst, nil},

		// ---- explicit relations win (GRP-1, GRP-7)
		{"a reply a day later is appended, whatever the window", Event{at(86400), textOnly}, nil,
			&Related{NoteID: noteA, PartID: partA, Active: true, Reason: ReasonReply}, Existing, ReasonReply, nil},
		{"a thread message is appended too", Event{at(3600), mediaOnly}, nil,
			&Related{NoteID: noteA, PartID: partA, Active: true, Reason: ReasonThread}, Existing, ReasonThread, nil},
		{"a reply beats automatic grouping with another note", Event{at(1), textOnly}, &Candidate{NoteID: uuid.New(), LastPartAt: at(0)},
			&Related{NoteID: noteA, PartID: partA, Active: true, Reason: ReasonReply}, Existing, ReasonReply, nil},
		{"a reply to a message of a dismissed note starts a new note that records the relation (GRP-7)", Event{at(2), textOnly}, noteWith(at(0), false),
			&Related{NoteID: noteA, PartID: partA, Active: false, Reason: ReasonReply}, New, ReasonFirst, &partA},

		// ---- the window (GRP-5): measured on platform timestamps from the last part
		{"exactly at the window edge still groups", Event{at(60), mediaOnly}, noteWith(at(0), false), nil, Existing, ReasonMedia, nil},
		{"one nanosecond later does not", Event{t0.Add(window + time.Nanosecond), mediaOnly}, noteWith(t0, false), nil, New, ReasonFirst, nil},
		{"the window runs from the LAST part, so a slow burst keeps growing", Event{at(110), mediaOnly}, noteWith(at(55), false), nil, Existing, ReasonMedia, nil},
		{"an event dated before the last part is not grouped (out-of-order backlog)", Event{at(-1), mediaOnly}, noteWith(at(0), false), nil, New, ReasonFirst, nil},
		{"the same instant groups", Event{at(0), mediaOnly}, noteWith(at(0), false), nil, Existing, ReasonMedia, nil},

		// ---- text and mixed messages (GRP-2b, GRP-4)
		{"a caption after the window is a note of its own", Event{at(90), textOnly}, noteWith(at(0), false), nil, New, ReasonFirst, nil},
		{"a message with text and a file is a caption when the note has none", Event{at(10), mixed}, noteWith(at(0), false), nil, Existing, ReasonMedia, nil},
		{"a message with text and a file does not add a second text body", Event{at(10), mixed}, noteWith(at(0), true), nil, New, ReasonFirst, nil},
		{"media never violates the one-text rule, even after a caption", Event{at(10), mediaOnly}, noteWith(at(0), true), nil, Existing, ReasonMedia, nil},

		// ---- degenerate input
		{"an event with no content still creates a note rather than being lost", Event{at(0), Content{}}, nil, nil, New, ReasonFirst, nil},
		{"no candidate, media only", Event{at(0), mediaOnly}, nil, nil, New, ReasonFirst, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(c.ev, c.cand, c.rel, window)
			if got.Target != c.want || got.Reason != c.reason {
				t.Fatalf("got %v/%q, want %v/%q", got.Target, got.Reason, c.want, c.reason)
			}
			if c.want == Existing {
				wantNote := noteA
				if c.cand != nil && c.rel == nil {
					wantNote = c.cand.NoteID
				}
				if got.NoteID != wantNote {
					t.Fatalf("note %s, want %s", got.NoteID, wantNote)
				}
			}
			if (c.relPart == nil) != (got.RelatedPartID == nil) || (c.relPart != nil && *got.RelatedPartID != *c.relPart) {
				t.Fatalf("related part: got %v, want %v", got.RelatedPartID, c.relPart)
			}
		})
	}
}

// The window is a parameter (a per-user setting): a longer one groups what a shorter one does not.
func TestWindowIsConfigurable(t *testing.T) {
	ev := Event{at(120), mediaOnly}
	cand := noteWith(at(0), false)
	if Decide(ev, cand, nil, time.Minute).Target != New {
		t.Fatal("60 s window grouped an event 120 s later")
	}
	if Decide(ev, cand, nil, 3*time.Minute).Target != Existing {
		t.Fatal("180 s window did not group an event 120 s later")
	}
	if Decide(ev, cand, nil, 0).Target != New {
		t.Fatal("a zero window must disable automatic grouping")
	}
}

// Every decision is explainable: it carries the reason recorded on the part (GRP-8).
func TestEveryDecisionHasAKnownReason(t *testing.T) {
	known := map[string]bool{ReasonFirst: true, ReasonReply: true, ReasonThread: true, ReasonMedia: true}
	for _, d := range []Decision{
		Decide(Event{at(0), textOnly}, nil, nil, window),
		Decide(Event{at(1), mediaOnly}, noteWith(at(0), false), nil, window),
		Decide(Event{at(1), textOnly}, nil, &Related{NoteID: noteA, Active: true, Reason: ReasonThread}, window),
	} {
		if !known[d.Reason] {
			t.Fatalf("unknown reason %q", d.Reason)
		}
	}
}
