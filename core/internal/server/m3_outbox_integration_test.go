//go:build integration

package server_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type outboxItem struct {
	ID             string         `json:"id"`
	Kind           string         `json:"kind"`
	ExternalUserID string         `json:"external_user_id"`
	ConversationID string         `json:"conversation_id"`
	Payload        map[string]any `json:"payload"`
	Attempts       int            `json:"attempts"`
}

func (s *stack) claim(key string, wait string) []outboxItem {
	s.t.Helper()
	res := s.botDo(key, "GET", "/bot/v1/outbox?limit=10"+wait, nil)
	if res.Status != 200 {
		s.t.Fatalf("claim: %d %s", res.Status, res.Body)
	}
	var out struct {
		Items []outboxItem `json:"items"`
	}
	res.JSON(s.t, &out)
	return out.Items
}

func (s *stack) report(key, id string, body map[string]any) int {
	s.t.Helper()
	return s.botDo(key, "POST", "/bot/v1/outbox/"+id+"/result", body).Status
}

// enqueue adds an item the way the reminder job will (M5).
func (s *stack) enqueue(instance string, user *uuid.UUID, kind, ext string) string {
	s.t.Helper()
	id := uuid.NewString()
	payload, _ := json.Marshal(map[string]any{"text": "hello"})
	if _, err := s.db.Admin.Exec(context.Background(), `insert into bot_outbox (id, bot_instance_id, kind, user_id, external_user_id, conversation_id, payload)
		values ($1, $2, $3, $4, $5, '!room', $6)`, id, instance, kind, user, ext, payload); err != nil {
		s.t.Fatal(err)
	}
	return id
}

func (s *stack) instanceID(name string) string {
	s.t.Helper()
	var id string
	if err := s.db.Admin.QueryRow(context.Background(), `select id::text from bot_instances where name = $1`, name).Scan(&id); err != nil {
		s.t.Fatal(err)
	}
	return id
}

// Two workers polling at once never receive the same item; an unacknowledged item comes back after
// its lease without using up an attempt; results move it on (BOT-11, BOT-12, BOT-13).
func TestOutboxClaimLeaseAndResults(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	inst := s.instanceID("m")
	ch := s.chatter("alice", key)
	var uid uuid.UUID
	_ = s.db.Admin.QueryRow(context.Background(), `select id from users where username = 'alice'`).Scan(&uid)
	for range 6 {
		s.enqueue(inst, &uid, "notice", ch.ext)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]int{}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, it := range s.claim(key, "") {
				mu.Lock()
				seen[it.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != 6 {
		t.Fatalf("claimed %d of 6 items", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("item %s handed to %d workers", id, n)
		}
	}
	if got := s.claim(key, ""); len(got) != 0 {
		t.Fatalf("claimed items offered again: %d", len(got))
	}

	// The bot dies without answering: after the lease the items come back, with the same attempt count.
	if _, err := s.db.Admin.Exec(context.Background(), `update bot_outbox set lease_expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	again := s.claim(key, "")
	if len(again) != 6 || again[0].Attempts != 0 {
		t.Fatalf("after lease expiry: %d items, attempts %d", len(again), again[0].Attempts)
	}

	first, second, third := again[0].ID, again[1].ID, again[2].ID
	if st := s.report(key, first, map[string]any{"state": "delivered", "message_ids": []string{"$sent1"}}); st != 204 {
		t.Fatalf("delivered: %d", st)
	}
	if s.count(`select count(*) from outbox_messages where message_id = '$sent1' and outbox_id = $1`, first) != 1 {
		t.Fatal("the sent message id must be remembered (BOT-13)")
	}
	// A transient failure retries later and counts as an attempt; nothing is offered before then.
	if st := s.report(key, second, map[string]any{"state": "failed_transient", "reason": "homeserver down"}); st != 204 {
		t.Fatalf("transient: %d", st)
	}
	if s.count(`select count(*) from bot_outbox where id = $1 and state = 'queued' and attempts = 1 and next_attempt_at > now()`, second) != 1 {
		t.Fatal("a transient failure must requeue with a delay")
	}
	// A permanent failure gives up and tells the user in the app (CORE-R9).
	if st := s.report(key, third, map[string]any{"state": "failed_permanent", "reason": "room gone"}); st != 204 {
		t.Fatalf("permanent: %d", st)
	}
	if s.count(`select count(*) from notifications where kind = 'delivery_failed'`) != 1 {
		t.Fatal("the user must be told about a message that could not be delivered")
	}
	// A result is accepted once: the item is no longer claimed.
	if st := s.report(key, first, map[string]any{"state": "delivered"}); st != 404 {
		t.Fatalf("second result: %d", st)
	}
	if st := s.report(key, uuid.NewString(), map[string]any{"state": "delivered"}); st != 404 {
		t.Fatalf("unknown item: %d", st)
	}
	if st := s.report(key, again[3].ID, map[string]any{"state": "bogus"}); st != 400 {
		t.Fatalf("bad state: %d", st)
	}
}

// One bot instance cannot see or answer another's items, and a disabled user's items wait (SEC-BOT-1, SEC-BOT-13).
func TestOutboxIsolationAndDisabledUsers(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	other := s.makeBot("n", "other.example")
	inst := s.instanceID("m")
	ch := s.chatter("alice", key)
	var uid uuid.UUID
	_ = s.db.Admin.QueryRow(context.Background(), `select id from users where username = 'alice'`).Scan(&uid)
	id := s.enqueue(inst, &uid, "reminder", ch.ext)

	if got := s.claim(other, ""); len(got) != 0 {
		t.Fatal("another bot instance received the item")
	}
	if got := s.claim(key, ""); len(got) != 1 || got[0].ID != id || got[0].Kind != "reminder" {
		t.Fatalf("own claim: %+v", got)
	}
	if st := s.report(other, id, map[string]any{"state": "delivered"}); st != 404 {
		t.Fatalf("foreign result: %d", st)
	}
	if s.count(`select count(*) from bot_outbox where state = 'claimed'`) != 1 {
		t.Fatal("a foreign result changed the item")
	}
	if st := s.botDo("", "GET", "/bot/v1/outbox", nil).Status; st != 401 {
		t.Fatalf("no key: %d", st)
	}

	// While the user is disabled nothing is sent to them; it flows again afterwards.
	if _, err := s.db.Admin.Exec(context.Background(), `update bot_outbox set state = 'queued', lease_expires_at = null`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Admin.Exec(context.Background(), `update users set status = 'disabled' where id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if got := s.claim(key, ""); len(got) != 0 {
		t.Fatal("an item for a disabled user was handed out")
	}
	if _, err := s.db.Admin.Exec(context.Background(), `update users set status = 'active' where id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if got := s.claim(key, ""); len(got) != 1 {
		t.Fatal("the item must flow again")
	}
}

// The long poll returns as soon as an item is queued, and an empty poll returns after the wait (BOT-12).
func TestOutboxLongPoll(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	inst := s.instanceID("m")
	ch := s.chatter("alice", key)
	var uid uuid.UUID
	_ = s.db.Admin.QueryRow(context.Background(), `select id from users where username = 'alice'`).Scan(&uid)

	start := time.Now()
	if got := s.claim(key, "&wait=1"); len(got) != 0 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("an empty poll must wait: %d items after %v", len(got), time.Since(start))
	}
	got := make(chan []outboxItem, 1)
	go func() { got <- s.claim(key, "&wait=20") }()
	time.Sleep(300 * time.Millisecond)
	s.enqueue(inst, &uid, "notice", ch.ext)
	if _, err := s.db.Admin.Exec(context.Background(), `select pg_notify('nk_outbox', $1)`, inst); err != nil {
		t.Fatal(err)
	}
	select {
	case items := <-got:
		if len(items) != 1 {
			t.Fatalf("woken with %d items", len(items))
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the long poll was not woken by the notification")
	}
}

// Expired items are given up and reported to the user (docs/design/05 section 4.4).
func TestOutboxExpiry(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	inst := s.instanceID("m")
	ch := s.chatter("alice", key)
	var uid uuid.UUID
	_ = s.db.Admin.QueryRow(context.Background(), `select id from users where username = 'alice'`).Scan(&uid)
	old := s.enqueue(inst, &uid, "reminder", ch.ext)
	fresh := s.enqueue(inst, &uid, "reminder", ch.ext)
	if _, err := s.db.Admin.Exec(context.Background(), `update bot_outbox set created_at = now() - interval '8 days' where id = $1`, old); err != nil {
		t.Fatal(err)
	}
	if err := s.svc.Outbox.Expire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.count(`select count(*) from bot_outbox where id = $1 and state = 'expired'`, old) != 1 || s.count(`select count(*) from bot_outbox where id = $1 and state = 'queued'`, fresh) != 1 {
		t.Fatal("only the old item may expire")
	}
	if s.count(`select count(*) from notifications where kind = 'delivery_failed'`) != 1 {
		t.Fatal("the user must be told")
	}
}
