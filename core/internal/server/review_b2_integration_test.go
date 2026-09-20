//go:build integration

package server_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A replayed reply command (the bot crashed before it committed its sync token) changes nothing: one
// reminder, and a repeating one is skipped once (BOT-7, CR-010).
// del calls DELETE and checks the status.
func (u *appUser) del(path string, want int) {
	u.t.Helper()
	if res := u.c.do("DELETE", path, nil); res.Status != want {
		u.t.Fatalf("DELETE %s: %d %s", path, res.Status, res.Body)
	}
}

func TestReplayedReplyCommandsChangeNothing(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	note := ch.send("$m1", 0, text("water the plants"))
	first := ch.command("remind", "in 3 hours", "$m1", "$c1")
	again := ch.command("remind", "in 3 hours", "$m1", "$c1") // the same message, delivered again
	if !first.OK || again.reply() != first.reply() {
		t.Fatalf("replay answered differently: %+v vs %+v", first, again)
	}
	if got := ch.reminderOf(*note.NoteID); len(got) != 1 {
		t.Fatalf("a replayed !remind made %d reminders", len(got))
	}
	if second := ch.command("remind", "in 4 hours", "$m1", "$c2"); !second.OK || len(ch.reminderOf(*note.NoteID)) != 2 {
		t.Fatalf("a different message is a different command: %+v", second)
	}

	// !done on a repeating reminder skips one occurrence, however often the message is delivered.
	n := ch.note("", "take the tablets", nil)
	due := time.Now().Add(time.Hour)
	r := ch.remind(n.ID, due, "FREQ=DAILY")
	s.clockAt(due.Add(time.Minute))
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 1 {
		t.Fatalf("fired %d", fired)
	}
	if _, err := s.db.Admin.Exec(t.Context(), `update bot_outbox set next_attempt_at = now()`); err != nil {
		t.Fatal(err)
	}
	for _, it := range s.claim(key, "") {
		s.report(key, it.ID, map[string]any{"state": "delivered", "message_ids": []string{"$rem1"}})
	}
	s.clockAt(time.Now())
	ch.command("done", "", "$rem1", "$d1")
	afterFirst := ch.reminderOf(n.ID)[0].DueAt
	ch.command("done", "", "$rem1", "$d1")
	if got := ch.reminderOf(n.ID)[0]; !got.DueAt.Equal(afterFirst) || got.ID != r.ID {
		t.Fatalf("a replayed !done skipped another occurrence: %v then %v", afterFirst, got.DueAt)
	}
}

// "Nothing from before linking" is judged on the platform's clock: a homeserver that runs behind Core
// must not make the first notes look like history (MX-10, CR-011).
func TestLinkTimeUsesThePlatformClock(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	u := s.appUser("carol")
	insts, _ := s.svc.Bots.ListInstances(context.Background())
	var uid string
	if err := s.db.Admin.QueryRow(context.Background(), `select id::text from users where username = 'carol'`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	var pc struct {
		Code string `json:"code"`
	}
	u.post("/api/v1/me/pairing-codes", map[string]any{"bot_instance_id": insts[0].ID.String()}, 201, &pc)
	ext := "@carol:example.org"
	// The homeserver's clock is two minutes behind Core's.
	behind := time.Now().Add(-2 * time.Minute).UTC()
	res := s.botDo(key, "POST", "/bot/v1/commands", map[string]any{"command": "link", "args": pc.Code, "sender": ext, "conversation": "!carol",
		"message_id": "$link", "timestamp": behind.Format(time.RFC3339Nano)})
	if res.Status != 200 {
		t.Fatalf("link: %d %s", res.Status, res.Body)
	}
	send := func(id string, at time.Time) string {
		r := s.botDo(key, "POST", "/bot/v1/events", map[string]any{"event_id": id, "kind": "message_created", "sender": ext, "conversation": "!carol",
			"message_id": id, "timestamp": at.UTC().Format(time.RFC3339Nano), "parts": []map[string]any{text("first note")}})
		if r.Status != 200 {
			t.Fatalf("event: %d %s", r.Status, r.Body)
		}
		var out eventOut
		r.JSON(t, &out)
		return out.Result
	}
	if got := send("$after", behind.Add(5*time.Second)); got != "created" {
		t.Fatalf("a message sent after the link was %s", got)
	}
	if got := send("$before", behind.Add(-time.Minute)); got != "ignored" {
		t.Fatalf("a message from before the link was %s", got)
	}
}

// What is still waiting to be said in a chat does not go out after the link was revoked or the reminders
// were switched off, and a deleted reminder or note is not delivered afterwards (CR-012, SEC-DATA-5).
func TestQueuedDeliveriesAreCancelledWithTheirReason(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	due := time.Now().Add(time.Hour)
	n1 := ch.note("", "secret plan one", nil)
	n2 := ch.note("", "secret plan two", nil)
	n3 := ch.note("", "secret plan three", nil)
	ch.remind(n1.ID, due, "")
	r2 := ch.remind(n2.ID, due, "")
	ch.remind(n3.ID, due, "")
	s.clockAt(due.Add(time.Minute))
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 3 {
		t.Fatalf("fired %d", fired)
	}
	queued := func() int {
		return s.count(`select count(*) from bot_outbox where kind = 'reminder' and state = 'queued'`)
	}
	if queued() != 3 {
		t.Fatalf("queued %d", queued())
	}
	// Deleting a reminder cancels its delivery, and the text is blanked with it.
	ch.del("/api/v1/reminders/"+r2.ID, 204)
	if queued() != 2 {
		t.Fatalf("after deleting a reminder: %d queued", queued())
	}
	// Deleting a note for good does the same.
	ch.post("/api/v1/notes/"+n3.ID+"/dismiss", nil, 200, nil)
	ch.del("/api/v1/notes/"+n3.ID, 204)
	if queued() != 1 {
		t.Fatalf("after deleting a note: %d queued", queued())
	}
	// Unlinking the chat cancels what is left, before the goodbye.
	var identity string
	if err := s.db.Admin.QueryRow(t.Context(), `select id::text from external_identities`).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	ch.del("/api/v1/me/identities/"+identity, 204)
	if queued() != 0 {
		t.Fatalf("after unlinking: %d reminders still queued", queued())
	}
	for _, it := range s.claim(key, "") {
		if it.Kind != "lifecycle" && it.Kind != "notice" {
			t.Fatalf("a %s was still delivered after the unlink", it.Kind)
		}
	}
	// No note text stays behind in cancelled items (CR-021).
	if left := s.count(`select count(*) from bot_outbox where kind = 'reminder' and payload::text like '%secret%'`); left != 0 {
		t.Fatalf("%d cancelled items still hold note text", left)
	}
}

// Finished items lose their text at once and are purged after the retention period (CR-021).
func TestFinishedOutboxItemsAreBlankedAndPurged(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	n := ch.note("", "private text of a note", nil)
	due := time.Now().Add(time.Hour)
	ch.remind(n.ID, due, "")
	s.clockAt(due.Add(time.Minute))
	_, _ = s.svc.Reminders.FireDue(t.Context())
	if _, err := s.db.Admin.Exec(t.Context(), `update bot_outbox set next_attempt_at = now()`); err != nil {
		t.Fatal(err)
	}
	items := s.claim(key, "")
	if len(items) != 1 || !strings.Contains(fmt.Sprint(items[0].Payload), "private text") {
		t.Fatalf("claimed: %+v", items)
	}
	if st := s.report(key, items[0].ID, map[string]any{"state": "delivered", "message_ids": []string{"$sent1"}}); st != 204 {
		t.Fatalf("report: %d", st)
	}
	if left := s.count(`select count(*) from bot_outbox where payload::text like '%private%'`); left != 0 {
		t.Fatal("a delivered item kept the note text")
	}
	// Reminder replies still resolve while the item exists, and the item goes after 30 days.
	if _, err := s.db.Admin.Exec(t.Context(), `update bot_outbox set finished_at = now() - interval '31 days'`); err != nil {
		t.Fatal(err)
	}
	if err := s.runHousekeeping(); err != nil {
		t.Fatal(err)
	}
	if s.count(`select count(*) from bot_outbox`) != 0 || s.count(`select count(*) from outbox_messages`) != 0 {
		t.Fatal("old finished items were not purged")
	}
}

// One reminder whose firing fails does not hold back the rest, and is set aside instead of being
// claimed first in every cycle (CR-020).
func TestFailingReminderDoesNotStarveTheOthers(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	due := time.Now().Add(time.Hour)
	bad, good := ch.note("", "will break", nil), ch.note("", "fine", nil)
	rb := ch.remind(bad.ID, due.Add(-time.Minute), "")
	ch.remind(good.ID, due, "")
	// A row the firing transaction cannot handle: the notification insert violates a constraint.
	if _, err := s.db.Admin.Exec(t.Context(), `create or replace function nk_test_break() returns trigger language plpgsql as $$ begin
		if new.payload::text like '%will break%' then raise exception 'poisoned'; end if; return new; end $$;
		create trigger nk_test_break before insert on notifications for each row execute function nk_test_break();`); err != nil {
		t.Fatal(err)
	}
	s.clockAt(due.Add(time.Minute))
	fired, err := s.svc.Reminders.FireDue(t.Context())
	if err == nil || fired != 1 {
		t.Fatalf("fired %d, err %v: the healthy reminder must fire and the failure must be reported", fired, err)
	}
	if !strings.Contains(err.Error(), rb.ID) {
		t.Fatalf("the error does not name the reminder: %v", err)
	}
	// The failing one waits, so the next cycle does not start with it again.
	var until *time.Time
	if e := s.db.Admin.QueryRow(t.Context(), `select claimed_until from reminders where id = $1`, rb.ID).Scan(&until); e != nil || until == nil || !until.After(time.Now().Add(time.Hour)) {
		t.Fatalf("claimed_until = %v (%v): it should be pushed out", until, e)
	}
}
