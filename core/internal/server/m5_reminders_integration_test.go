//go:build integration

package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type reminderJSON struct {
	ID          string     `json:"id"`
	NoteID      string     `json:"note_id"`
	DueAt       time.Time  `json:"due_at"`
	Rrule       *string    `json:"rrule"`
	State       string     `json:"state"`
	LastFiredAt *time.Time `json:"last_fired_at"`
	Excerpt     string     `json:"excerpt"`
}

func (u *appUser) remind(note string, due time.Time, rrule string) reminderJSON {
	u.t.Helper()
	body := map[string]any{"due_at": due.UTC().Format(time.RFC3339Nano)}
	if rrule != "" {
		body["rrule"] = rrule
	}
	var r reminderJSON
	u.post("/api/v1/notes/"+note+"/reminders", body, 201, &r)
	return r
}

func (u *appUser) reminderOf(note string) []reminderJSON {
	u.t.Helper()
	var n struct {
		Reminders []reminderJSON `json:"reminders"`
	}
	u.get("/api/v1/notes/"+note, &n)
	return n.Reminders
}

// at makes the reminder service, and only it, believe it is later.
func (s *stack) clockAt(t time.Time) {
	s.svc.Reminders.Now = func() time.Time { return t }
	s.svc.Notes.Now = func() time.Time { return t } // restoring a note re-arms by the same clock
}

func (s *stack) outboxItems(kind string) []map[string]any {
	s.t.Helper()
	rows, err := s.db.Admin.Query(context.Background(), `select payload::text, external_user_id, conversation_id, coalesce(reminder_id::text, '') from bot_outbox where kind = $1 order by created_at`, kind)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var payload, ext, conv, rid string
		_ = rows.Scan(&payload, &ext, &conv, &rid)
		m := map[string]any{}
		_ = json.Unmarshal([]byte(payload), &m)
		m["external_user_id"], m["conversation_id"], m["reminder_id"] = ext, conv, rid
		out = append(out, m)
	}
	return out
}

type notificationsJSON struct {
	Items []struct {
		ID      string         `json:"id"`
		Kind    string         `json:"kind"`
		Payload map[string]any `json:"payload"`
		ReadAt  *time.Time     `json:"read_at"`
	} `json:"items"`
	Unread int `json:"unread"`
}

// A reminder on a note fires once when due: an outbox item for the reminder-target chat with the note
// text, a deep link and the files, and a notification in the app (CORE-R1, CORE-R3, CORE-R5, CORE-R6, CORE-R9).
func TestReminderFiresToTheChatAndTheApp(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	n, att := ch.noteWithFile("Call the dentist about the crown", "invoice.pdf", "application/pdf", []byte("%PDF-1.4 fake"))
	due := time.Now().Add(time.Hour)
	r := ch.remind(n.ID, due, "")
	if got := ch.reminderOf(n.ID); len(got) != 1 || got[0].ID != r.ID || got[0].State != "pending" {
		t.Fatalf("the note must list its reminder: %+v", got)
	}

	// Not due yet: nothing happens, however often the job runs.
	if fired, err := s.svc.Reminders.FireDue(t.Context()); err != nil || fired != 0 {
		t.Fatalf("early: %d %v", fired, err)
	}
	s.clockAt(due.Add(30 * time.Second))
	// Many replicas at once fire it exactly once (CORE-R5).
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.svc.Reminders.FireDue(context.Background()) }()
	}
	wg.Wait()
	items := s.outboxItems("reminder")
	if len(items) != 1 {
		t.Fatalf("%d reminder deliveries, want exactly 1", len(items))
	}
	it := items[0]
	text, _ := it["text"].(string)
	if it["external_user_id"] != ch.ext || it["conversation_id"] != ch.conv || it["reminder_id"] != r.ID || it["late"] != false ||
		!strings.Contains(text, "Call the dentist about the crown") || !strings.Contains(text, "https://app.example.net/notes/"+n.ID) || !strings.Contains(text, "1 file") {
		t.Fatalf("delivery: %+v", it)
	}
	atts, _ := it["attachments"].([]any)
	if len(atts) != 1 || atts[0].(map[string]any)["id"] != att || atts[0].(map[string]any)["filename"] != "invoice.pdf" {
		t.Fatalf("attachments: %+v", it["attachments"])
	}
	// It stays on the note as fired (CORE-R8) and is in the app's notifications (CORE-R9).
	if got := ch.reminderOf(n.ID); got[0].State != "fired" || got[0].LastFiredAt == nil {
		t.Fatalf("after firing: %+v", got)
	}
	var notes notificationsJSON
	ch.appUser.get("/api/v1/notifications", &notes)
	if notes.Unread != 1 || len(notes.Items) != 1 || notes.Items[0].Kind != "reminder" || !strings.Contains(notes.Items[0].Payload["excerpt"].(string), "dentist") {
		t.Fatalf("notifications: %+v", notes)
	}
	ch.post("/api/v1/notifications/read", nil, 204, nil)
	ch.appUser.get("/api/v1/notifications?unread=true", &notes)
	if notes.Unread != 0 || len(notes.Items) != 0 {
		t.Fatalf("after reading: %+v", notes)
	}
	// Firing again does nothing: a one-off fires once.
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 0 || len(s.outboxItems("reminder")) != 1 {
		t.Fatal("a fired reminder fired again")
	}
}

// A delivery that is more than five minutes past due says so (CORE-R4), and only the chats chosen as
// reminder targets get it; with no chat, the app alone is told (CORE-R3).
func TestReminderTargetsAndLateDeliveries(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	n := ch.note("", "water the plants", nil)
	due := time.Now().Add(time.Hour)
	ch.remind(n.ID, due, "")
	s.clockAt(due.Add(3 * time.Hour)) // the bot or Core was down for hours
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 1 {
		t.Fatal("not fired")
	}
	if it := s.outboxItems("reminder")[0]; it["late"] != true || !strings.Contains(it["text"].(string), "late") {
		t.Fatalf("late delivery: %+v", it)
	}

	// Unchecking the chat as a target: the next reminder only reaches the app.
	var ids struct{ Items []struct{ ID string } }
	ch.appUser.get("/api/v1/me/identities", &ids)
	var ident struct {
		ReminderTarget bool `json:"reminder_target"`
	}
	if res := ch.c.do("PATCH", "/api/v1/me/identities/"+ids.Items[0].ID, map[string]any{"reminder_target": false}); res.Status != 200 {
		t.Fatalf("patch: %d %s", res.Status, res.Body)
	} else {
		res.JSON(t, &ident)
	}
	if ident.ReminderTarget {
		t.Fatal("the flag was not cleared")
	}
	s.clockAt(time.Now())
	m := ch.note("", "second", nil)
	ch.remind(m.ID, time.Now().Add(time.Minute), "")
	s.clockAt(time.Now().Add(2 * time.Minute))
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 1 || len(s.outboxItems("reminder")) != 1 {
		t.Fatalf("no target: fired %d, deliveries %d", fired, len(s.outboxItems("reminder")))
	}
	var notes notificationsJSON
	ch.appUser.get("/api/v1/notifications", &notes)
	if len(notes.Items) != 2 {
		t.Fatalf("the app must be told either way: %d", len(notes.Items))
	}
	// Somebody else's identity cannot be changed (SEC-ISO-3).
	bob := s.appUser("bob")
	if res := bob.c.do("PATCH", "/api/v1/me/identities/"+ids.Items[0].ID, map[string]any{"reminder_target": true}); res.Status != 404 {
		t.Fatalf("foreign identity: %d", res.Status)
	}
}

// Times, repetition and limits are checked on the server, for hand-made requests too (CORE-R1, CORE-R10).
func TestReminderValidation(t *testing.T) {
	s := newStack(t)
	u, bob := s.appUser("alice"), s.appUser("bob")
	n := u.note("", "hello", nil)
	post := func(who *appUser, note string, body any) response {
		return who.c.do("POST", "/api/v1/notes/"+note+"/reminders", body)
	}
	soon := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for name, tc := range map[string]struct {
		body any
		want int
	}{
		"past":           {map[string]any{"due_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}, 400},
		"far future":     {map[string]any{"due_at": time.Now().AddDate(20, 0, 0).UTC().Format(time.RFC3339)}, 400},
		"no time":        {map[string]any{}, 400},
		"bad time":       {map[string]any{"due_at": "tomorrow"}, 400},
		"unknown rule":   {map[string]any{"due_at": soon, "rrule": "FREQ=YEARLY"}, 400},
		"injection rule": {map[string]any{"due_at": soon, "rrule": "FREQ=DAILY;X=1"}, 400},
		"good repeat":    {map[string]any{"due_at": soon, "rrule": "FREQ=WEEKLY;BYDAY=MO,WE"}, 201},
	} {
		if res := post(u, n.ID, tc.body); res.Status != tc.want {
			t.Errorf("%s: %d %s", name, res.Status, res.Body)
		}
	}
	if res := post(bob, n.ID, map[string]any{"due_at": soon}); res.Status != 404 {
		t.Fatalf("foreign note: %d", res.Status)
	}
	// A note in the Trash cannot get a reminder, and reminders per note are capped.
	gone := u.note("", "gone", nil)
	u.post("/api/v1/notes/"+gone.ID+"/dismiss", nil, 200, nil)
	if res := post(u, gone.ID, map[string]any{"due_at": soon}); res.Status != 409 {
		t.Fatalf("dismissed: %d", res.Status)
	}
	for range 25 {
		if res := post(u, n.ID, map[string]any{"due_at": soon}); res.Status == 429 {
			return
		}
	}
	t.Fatal("the number of reminders on one note must be capped")
}

// Dismissing suspends reminders; restoring re-arms those still ahead, and cancels those that fell due
// meanwhile without sending them (CORE-R7).
func TestDismissSuspendsAndRestoreRearms(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n := u.note("", "ticket", nil)
	soon, later := time.Now().Add(time.Hour), time.Now().Add(48*time.Hour)
	a, b := u.remind(n.ID, soon, ""), u.remind(n.ID, later, "")
	rec := u.remind(n.ID, soon, "FREQ=DAILY")

	u.post("/api/v1/notes/"+n.ID+"/dismiss", nil, 200, nil)
	states := func() map[string]string {
		m := map[string]string{}
		for _, r := range u.reminderOf(n.ID) {
			m[r.ID] = r.State
		}
		return m
	}
	if st := states(); st[a.ID] != "suspended" || st[b.ID] != "suspended" {
		t.Fatalf("after dismissing: %v", st)
	}
	s.clockAt(soon.Add(time.Hour)) // a fell due while the note was in the Trash
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 0 || len(s.outboxItems("reminder")) != 0 {
		t.Fatal("a reminder of a dismissed note fired")
	}
	u.post("/api/v1/notes/"+n.ID+"/restore", nil, 200, nil)
	st := states()
	if st[a.ID] != "cancelled" || st[b.ID] != "pending" || st[rec.ID] != "pending" {
		t.Fatalf("after restoring: %v", st)
	}
	for _, r := range u.reminderOf(n.ID) {
		if r.ID == rec.ID && !r.DueAt.After(soon.Add(time.Hour)) {
			t.Fatalf("a repeating reminder must move to its next time: %v", r.DueAt)
		}
	}
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 0 {
		t.Fatal("a cancelled reminder fired")
	}
}

// A repeating reminder fires, moves to its next time after now, and after an outage delivers once, not
// once per missed time (CORE-R10, CORE-R5).
func TestRepeatingReminderCollapsesMissedTimes(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alice", key)
	n := ch.note("", "take the vitamins", nil)
	first := time.Now().Add(time.Hour)
	r := ch.remind(n.ID, first, "FREQ=DAILY")
	s.clockAt(first.Add(5 * 24 * time.Hour)) // five days of outage
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 1 {
		t.Fatalf("fired %d", fired)
	}
	if len(s.outboxItems("reminder")) != 1 {
		t.Fatalf("%d deliveries after an outage, want one late one", len(s.outboxItems("reminder")))
	}
	got := ch.reminderOf(n.ID)[0]
	if got.ID != r.ID || got.State != "pending" || !got.DueAt.After(first.Add(5*24*time.Hour)) || got.DueAt.Sub(first.Add(5*24*time.Hour)) > 24*time.Hour+time.Minute {
		t.Fatalf("next occurrence: %+v", got)
	}
	// Ending it: delete clears it; nothing more fires.
	if res := ch.c.do("DELETE", "/api/v1/reminders/"+r.ID, nil); res.Status != 204 {
		t.Fatalf("delete: %d", res.Status)
	}
	s.clockAt(first.Add(30 * 24 * time.Hour))
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 0 {
		t.Fatal("a cleared reminder fired")
	}
}

// Snooze moves it, done ends it (a repeating one skips to its next time), and the app lists what is upcoming (CORE-R8, CORE-R9).
func TestSnoozeDoneAndUpcoming(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n := u.note("", "renew passport", nil)
	due := time.Now().Add(time.Hour)
	r := u.remind(n.ID, due, "")
	var list struct {
		Items []reminderJSON `json:"items"`
	}
	u.get("/api/v1/reminders", &list)
	if len(list.Items) != 1 || list.Items[0].Excerpt != "renew passport" {
		t.Fatalf("upcoming: %+v", list)
	}
	s.clockAt(due.Add(time.Minute))
	_, _ = s.svc.Reminders.FireDue(t.Context())
	later := time.Now().Add(3 * time.Hour)
	var snoozed reminderJSON
	u.post("/api/v1/reminders/"+r.ID+"/snooze", map[string]any{"until": later.UTC().Format(time.RFC3339Nano)}, 200, &snoozed)
	if snoozed.State != "pending" || snoozed.DueAt.Sub(later).Abs() > time.Second {
		t.Fatalf("snoozed: %+v", snoozed)
	}
	if res := u.c.do("POST", "/api/v1/reminders/"+r.ID+"/snooze", map[string]any{"until": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}); res.Status != 400 {
		t.Fatalf("snooze into the past: %d", res.Status)
	}
	var done reminderJSON
	u.post("/api/v1/reminders/"+r.ID+"/done", nil, 200, &done)
	if done.State != "done" {
		t.Fatalf("done: %+v", done)
	}
	u.get("/api/v1/reminders", &list)
	if len(list.Items) != 0 {
		t.Fatalf("a done reminder is not upcoming: %+v", list)
	}
	// Change a reminder: it arms again, with a new time and repetition, and an empty rule ends the repetition.
	var upd reminderJSON
	newDue := time.Now().Add(5 * time.Hour)
	if res := u.c.do("PATCH", "/api/v1/reminders/"+r.ID, map[string]any{"due_at": newDue.UTC().Format(time.RFC3339Nano), "rrule": "FREQ=DAILY"}); res.Status != 200 {
		t.Fatalf("patch: %d %s", res.Status, res.Body)
	} else {
		res.JSON(t, &upd)
	}
	if upd.State != "pending" || upd.Rrule == nil || *upd.Rrule != "FREQ=DAILY" {
		t.Fatalf("patched: %+v", upd)
	}
	upd = reminderJSON{}
	u.c.do("PATCH", "/api/v1/reminders/"+r.ID, map[string]any{"rrule": ""}).JSON(t, &upd)
	if upd.Rrule != nil {
		t.Fatalf("the repetition must be gone: %+v", upd)
	}
	// Done on a repeating reminder skips this time and keeps it armed.
	u.c.do("PATCH", "/api/v1/reminders/"+r.ID, map[string]any{"rrule": "FREQ=DAILY"})
	u.post("/api/v1/reminders/"+r.ID+"/done", nil, 200, &done)
	if done.State != "pending" || !done.DueAt.After(newDue) {
		t.Fatalf("done on a repeating reminder: %+v", done)
	}
	// Nobody else can touch it.
	bob := s.appUser("bob")
	for _, req := range [][]string{{"PATCH", ""}, {"DELETE", ""}, {"POST", "/snooze"}, {"POST", "/done"}} {
		if res := bob.c.do(req[0], "/api/v1/reminders/"+r.ID+req[1], map[string]any{"until": later.UTC().Format(time.RFC3339), "due_at": later.UTC().Format(time.RFC3339)}); res.Status != 404 {
			t.Errorf("%s %s by another user: %d", req[0], req[1], res.Status)
		}
	}
}

// A disabled user's reminders wait (SEC-BOT-13), and a user who cannot be found fires nothing.
func TestDisabledUsersReminderWaits(t *testing.T) {
	s := newStack(t)
	u := s.appUser("alice")
	n := u.note("", "later", nil)
	due := time.Now().Add(time.Hour)
	u.remind(n.ID, due, "")
	if _, err := s.db.Admin.Exec(t.Context(), `update users set status = 'disabled' where username = 'alice'`); err != nil {
		t.Fatal(err)
	}
	s.clockAt(due.Add(time.Minute))
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 0 {
		t.Fatal("a disabled user's reminder fired")
	}
	if _, err := s.db.Admin.Exec(t.Context(), `update users set status = 'active' where username = 'alice'`); err != nil {
		t.Fatal(err)
	}
	if fired, _ := s.svc.Reminders.FireDue(t.Context()); fired != 1 {
		t.Fatal("it must fire once the user is active again")
	}
}

var _ = uuid.Nil
