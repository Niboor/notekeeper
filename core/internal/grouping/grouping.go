// Package grouping decides whether a chat message starts a new note or joins an existing one
// (docs/design/04-ingestion.md sections 2.1 and 5, requirements GRP-1..GRP-9). The policy is one
// pure function with no database access: the ingest transaction supplies the facts and acts on
// the answer, so the policy can be tested as a table and replaced without touching the API or the
// bots (GRP-9).
package grouping

import (
	"time"

	"github.com/google/uuid"
)

// Reasons recorded on each part (GRP-8). They are also the values of note_parts.attach_reason.
const (
	ReasonFirst  = "first"
	ReasonReply  = "reply"
	ReasonThread = "thread"
	ReasonMedia  = "media-adjacency"
)

// Target says where the message goes.
type Target int

// Targets.
const (
	New      Target = iota // create a new note
	Existing               // append to an existing note
)

// Content describes what an event carries. Failed and unsupported attachments count as media:
// they are not text bodies, so they never violate the one-text-body rule (GRP-4).
type Content struct {
	HasText  bool
	HasMedia bool
}

// Event is the message being placed. Timestamp is the platform time, never the receive time, so
// a backlog groups exactly as it would have live (GRP-5, CORE-N18).
type Event struct {
	Timestamp time.Time
	Content   Content
}

// Candidate is the sender's most recent active note in this conversation.
type Candidate struct {
	NoteID     uuid.UUID
	LastPartAt time.Time // platform time of its most recent part
	HasText    bool      // it already has a text part
}

// Related is the note that owns the message this one replies to or continues a thread of.
type Related struct {
	NoteID uuid.UUID
	PartID uuid.UUID
	Active bool   // false when the note is in the Trash (GRP-7)
	Reason string // ReasonReply or ReasonThread
}

// Decision is the outcome.
type Decision struct {
	Target Target
	NoteID uuid.UUID // for Existing
	Reason string
	// RelatedPartID is set when a new note is created for a reply to a part of a dismissed note (GRP-7).
	RelatedPartID *uuid.UUID
}

// Decide applies the grouping policy:
//
//  1. An explicit relation (reply or thread) wins, with no time limit (GRP-1). If the related note
//     is in the Trash a new note is created that records the relation (GRP-7).
//  2. Otherwise a message joins the sender's adjacent note in the same conversation when it falls
//     within window of that note's last part (GRP-2, GRP-5):
//     - a media-only message always joins (photo bursts, a file after its caption: GRP-3);
//     - a message with text joins only if the note has no text yet, i.e. it is the caption of
//     media that came first (GRP-2b); two text messages are never merged by timing alone (GRP-4).
//  3. Otherwise a new note is created.
func Decide(ev Event, cand *Candidate, rel *Related, window time.Duration) Decision {
	if rel != nil {
		if rel.Active {
			return Decision{Target: Existing, NoteID: rel.NoteID, Reason: rel.Reason}
		}
		part := rel.PartID
		return Decision{Target: New, Reason: ReasonFirst, RelatedPartID: &part}
	}
	if cand != nil && window > 0 {
		gap := ev.Timestamp.Sub(cand.LastPartAt)
		if gap >= 0 && gap <= window {
			mediaOnly := ev.Content.HasMedia && !ev.Content.HasText
			if mediaOnly || (ev.Content.HasText && !cand.HasText) {
				return Decision{Target: Existing, NoteID: cand.NoteID, Reason: ReasonMedia}
			}
		}
	}
	return Decision{Target: New, Reason: ReasonFirst}
}
