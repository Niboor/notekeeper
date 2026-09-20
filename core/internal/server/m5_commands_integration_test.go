//go:build integration

package server_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

type cmdResult struct {
	OK       bool `json:"ok"`
	Feedback struct {
		React     *string `json:"react"`
		ReplyText *string `json:"reply_text"`
	} `json:"feedback"`
}

func (r cmdResult) reply() string {
	if r.Feedback.ReplyText == nil {
		return ""
	}
	return *r.Feedback.ReplyText
}

func (ch *chatter) command(name, args, replyTo, messageID string) cmdResult {
	ch.t.Helper()
	body := map[string]any{"command": name, "args": args, "sender": ch.ext, "conversation": ch.conv, "timestamp": time.Now().UTC().Format(time.RFC3339Nano)}
	if replyTo != "" {
		body["reply_to"] = replyTo
	}
	if messageID != "" {
		body["message_id"] = messageID
	}
	res := ch.s.botDo(ch.key, "POST", "/bot/v1/commands", body)
	if res.Status != 200 {
		ch.t.Fatalf("command: %d %s", res.Status, res.Body)
	}
	var out cmdResult
	res.JSON(ch.t, &out)
	return out
}

func within(t *testing.T, got, want time.Time, d time.Duration) {
	t.Helper()
	if got.Sub(want).Abs() > d {
		t.Fatalf("time %v, want %v", got, want)
	}
}

// `!remind <when>` as a reply sets a reminder on that message's note; `!remind <when> <text>` makes a
// new note with a reminder, once even if delivered twice; ordinary messages never make reminders
// (CORE-R11, CORE-R13, BOT-14, MX-8).
func TestRemindFromChat(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)

	note := ch.send("$m1", 0, text("book flights to Lisbon"))
	res := ch.command("remind", "in 3 hours", "$m1", "$c1")
	if !res.OK || !strings.Contains(res.reply(), "remind you") || res.Feedback.React == nil {
		t.Fatalf("remind as a reply: %+v", res)
	}
	rs := ch.reminderOf(*note.NoteID)
	if len(rs) != 1 {
		t.Fatalf("reminders: %+v", rs)
	}
	within(t, rs[0].DueAt, time.Now().Add(3*time.Hour), time.Minute)

	// A new note with a reminder, in one step.
	res = ch.command("remind", "in 2 hours call the dentist", "", "$c2")
	if !res.OK || !strings.Contains(res.reply(), "Saved as a note") {
		t.Fatalf("remind with text: %+v", res)
	}
	inbox := ch.inbox()
	var made noteJSON
	for _, n := range inbox {
		if len(n.Parts) == 1 && n.Parts[0].Text != nil && *n.Parts[0].Text == "call the dentist" {
			made = n
		}
	}
	if made.ID == "" {
		t.Fatalf("the note was not created: %d notes", len(inbox))
	}
	if r := ch.reminderOf(made.ID); len(r) != 1 {
		t.Fatalf("the new note's reminder: %+v", r)
	}
	// Editing or deleting the command message afterwards changes neither the note nor its reminder.
	dueBefore := ch.reminderOf(made.ID)[0].DueAt
	if out := ch.edit("$c2", time.Second, "!remind in 3 hours call the plumber"); out.Result != "ignored" {
		t.Fatalf("edit of a command message: %+v", out)
	}
	if out := ch.remove("$c2", 2*time.Second); out.Result != "ignored" {
		t.Fatalf("delete of a command message: %+v", out)
	}
	if got := ch.texts(ch.get(made.ID)); len(got) != 1 || got[0] != "call the dentist" || !ch.reminderOf(made.ID)[0].DueAt.Equal(dueBefore) {
		t.Fatalf("the note or its reminder was changed by an edit of the command message: %v", got)
	}
	// Delivered twice (the bot restarted): nothing more is created.
	ch.command("remind", "in 2 hours call the dentist", "", "$c2")
	if ch.notes() != 2 || ch.s.count(`select count(*) from reminders`) != 2 {
		t.Fatalf("a replayed command created things: %d notes, %d reminders", ch.notes(), ch.s.count(`select count(*) from reminders`))
	}
	// The command is not a note by itself, and the text made by it is not merged into the neighbour.
	if got := ch.texts(ch.get(*note.NoteID)); len(got) != 1 {
		t.Fatalf("the first note grew: %v", got)
	}

	// Plain messages that mention reminders are just notes (CORE-R11).
	before := ch.s.count(`select count(*) from reminders`)
	ch.send("$m2", time.Minute, text("remind me to buy milk tomorrow"))
	ch.send("$m3", 2*time.Minute, text("!remindme tomorrow"))
	if ch.s.count(`select count(*) from reminders`) != before {
		t.Fatal("an ordinary message created a reminder")
	}
}

// What cannot be understood creates nothing, and the reply says what to do (CORE-R11).
func TestRemindFailuresCreateNothing(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	ch.send("$m1", 0, text("a note"))
	for name, tc := range map[string]struct{ args, replyTo, want string }{
		"nothing":            {"", "", "When should I remind you"},
		"gibberish":          {"whenever you feel like it", "", "could not understand"},
		"in the past":        {"2020-01-01", "", "already passed"},
		"time only":          {"tomorrow 9am", "", "What should I remind you about"},
		"text after a reply": {"tomorrow 9am and more", "$m1", "Reply with just the time"},
		"unknown message":    {"tomorrow 9am", "$nope", "do not have that one"},
	} {
		res := ch.command("remind", tc.args, tc.replyTo, "")
		if res.OK || !strings.Contains(res.reply(), tc.want) {
			t.Errorf("%s: %+v", name, res)
		}
	}
	if ch.s.count(`select count(*) from reminders`) != 0 || ch.notes() != 1 {
		t.Fatalf("a failed command left something behind: %d reminders, %d notes", ch.s.count(`select count(*) from reminders`), ch.notes())
	}
	// A reminder on a note in the Trash is refused.
	n := ch.send("$m9", time.Minute, text("trashed"))
	ch.post("/api/v1/notes/"+*n.NoteID+"/dismiss", nil, 200, nil)
	if res := ch.command("remind", "tomorrow 9am", "$m9", ""); res.OK || !strings.Contains(res.reply(), "Trash") {
		t.Fatalf("trashed: %+v", res)
	}
}

// Replying to a reminder message with !snooze or !done acts on that reminder, and only on the reminders
// sent to that chat (CORE-R11c, BOT-13, SEC-BOT-11).
func TestSnoozeAndDoneFromChat(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	alice, bob := s.chatter("alice", key), s.chatter("bob", key)
	an, bn := alice.note("", "alice's errand", nil), bob.note("", "bob's errand", nil)
	due := time.Now().Add(time.Hour)
	ar, br := alice.remind(an.ID, due, ""), bob.remind(bn.ID, due, "")
	s.clockAt(due.Add(time.Minute))
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 2 {
		t.Fatalf("fired %d", fired)
	}
	// Each delivery targets its own user's conversation, and only that (SEC-BOT-11).
	for _, it := range s.outboxItems("reminder") {
		switch it["reminder_id"] {
		case ar.ID:
			if it["conversation_id"] != alice.conv || it["external_user_id"] != alice.ext {
				t.Fatalf("alice's reminder goes to %v %v", it["external_user_id"], it["conversation_id"])
			}
		case br.ID:
			if it["conversation_id"] != bob.conv || it["external_user_id"] != bob.ext {
				t.Fatalf("bob's reminder goes to %v %v", it["external_user_id"], it["conversation_id"])
			}
		default:
			t.Fatalf("unexpected delivery %v", it)
		}
	}
	// The bot delivers and reports the Matrix ids of what it sent. (The test clock ran ahead; the items are due now.)
	if _, err := s.db.Admin.Exec(t.Context(), `update bot_outbox set next_attempt_at = now()`); err != nil {
		t.Fatal(err)
	}
	for _, it := range s.claim(key, "") {
		id := fmt.Sprintf("$sent-%s", it.ExternalUserID)
		if st := s.report(key, it.ID, map[string]any{"state": "delivered", "message_ids": []string{id}}); st != 204 {
			t.Fatalf("report: %d", st)
		}
	}

	// A reply that is not to a reminder message is refused.
	if res := alice.command("snooze", "30m", "$m-random", ""); res.OK || !strings.Contains(res.reply(), "not one of my reminder messages") {
		t.Fatalf("unrelated reply: %+v", res)
	}
	if res := alice.command("snooze", "30m", "", ""); res.OK || !strings.Contains(res.reply(), "Reply to a reminder") {
		t.Fatalf("no reply: %+v", res)
	}
	// Bob cannot act on Alice's reminder, even knowing the id of the message that carried it (SEC-BOT-11).
	if res := bob.command("done", "", "$sent-"+alice.ext, ""); res.OK {
		t.Fatalf("bob acted on alice's reminder: %+v", res)
	}
	if got := alice.reminderOf(an.ID)[0]; got.State != "fired" {
		t.Fatalf("alice's reminder changed: %+v", got)
	}

	s.clockAt(time.Now())
	res := alice.command("snooze", "30m", "$sent-"+alice.ext, "")
	if !res.OK || !strings.Contains(res.reply(), "Snoozed until") {
		t.Fatalf("snooze: %+v %s", res, res.reply())
	}
	got := alice.reminderOf(an.ID)[0]
	if got.State != "pending" {
		t.Fatalf("after snooze: %+v", got)
	}
	within(t, got.DueAt, time.Now().Add(30*time.Minute), time.Minute)
	if res := alice.command("snooze", "sometime", "$sent-"+alice.ext, ""); res.OK || !strings.Contains(res.reply(), "could not understand") {
		t.Fatalf("bad duration: %+v", res)
	}
	res = alice.command("done", "", "$sent-"+alice.ext, "")
	if !res.OK || alice.reminderOf(an.ID)[0].State != "done" {
		t.Fatalf("done: %+v", res)
	}
	// Bob's is untouched.
	if got := bob.reminderOf(bn.ID)[0]; got.State != "fired" {
		t.Fatalf("bob's reminder changed: %+v", got)
	}
}

// A bot can fetch the files of a reminder it has claimed, for as long as it holds the claim, and never
// anything else of the user's (BOT-15, SEC-BOT-3).
func TestBotFetchesOnlyTheFilesOfItsClaimedReminder(t *testing.T) {
	s := newStack(t)
	key, other := s.makeBot("m", "example.org"), s.makeBot("n", "other.example")
	alice := s.chatter("alice", key)
	file := randomBytes(300_000)
	n, att := alice.noteWithFile("boarding pass", "pass.pdf", "application/pdf", file)
	_, unrelated := alice.noteWithFile("another note", "diary.txt", "text/plain", []byte("dear diary"))
	bob := s.appUser("bob")
	_, bobsFile := bob.noteWithFile("bobs", "bob.txt", "text/plain", []byte("bob"))
	due := time.Now().Add(time.Hour)
	alice.remind(n.ID, due, "")
	s.clockAt(due.Add(time.Minute))
	_, _ = s.svc.Reminders.FireDue(t.Context())
	if _, err := s.db.Admin.Exec(t.Context(), `update bot_outbox set next_attempt_at = now()`); err != nil {
		t.Fatal(err)
	}
	var itemID string
	if err := s.db.Admin.QueryRow(t.Context(), `select id::text from bot_outbox where kind = 'reminder'`).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	get := func(bearer, item, att string) response {
		return s.botDo(bearer, "GET", "/bot/v1/outbox/"+item+"/attachments/"+att, nil)
	}
	if get(key, itemID, att).Status != 404 {
		t.Fatal("a file of an item that is not claimed must not be served")
	}
	claimed := s.claim(key, "")
	if len(claimed) != 1 || claimed[0].ID != itemID {
		t.Fatalf("claim: %+v", claimed)
	}
	if res := get(key, itemID, att); res.Status != 200 || !bytesEqual(res.Body, file) {
		t.Fatalf("own file: %d, %d bytes", res.Status, len(res.Body))
	}
	for name, id := range map[string]string{"another file of the same user": unrelated, "another user's file": bobsFile, "made up": uuid4()} {
		if res := get(key, itemID, id); res.Status != 404 {
			t.Errorf("%s: %d", name, res.Status)
		}
	}
	if get(other, itemID, att).Status != 404 {
		t.Fatal("another bot instance fetched the file")
	}
	if get("", itemID, att).Status != 401 {
		t.Fatal("no key")
	}
	if st := s.report(key, itemID, map[string]any{"state": "delivered"}); st != 204 {
		t.Fatal(st)
	}
	if get(key, itemID, att).Status != 404 {
		t.Fatal("the file must be unreachable once the claim is over")
	}
}

func uuid4() string { return "00000000-0000-4000-8000-00000000abcd" }

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }
