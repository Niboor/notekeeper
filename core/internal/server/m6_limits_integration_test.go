//go:build integration

package server_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/config"
)

// A caller that asks too often is refused with a wait time, and only that caller (SEC-API-4, NFR-S4).
func TestUserAndAddressRateLimits(t *testing.T) {
	s := newStackWith(t, func(c *config.Config) { c.RateUserPerMin, c.RateIPPerMin = 30, 1_000_000 })
	busy, calm := s.appUser("busy"), s.appUser("calm")
	var refused *response
	for range 80 {
		if res := busy.c.do("GET", "/api/v1/me", nil); res.Status == 429 {
			refused = &res
			break
		}
	}
	if refused == nil || refused.Header.Get("Retry-After") == "" || refused.Code() != "rate_limited" {
		t.Fatalf("the per-user limit never applied: %+v", refused)
	}
	if calm.c.do("GET", "/api/v1/me", nil).Status != 200 {
		t.Fatal("one user's limit must not affect another")
	}

	s2 := newStackWith(t, func(c *config.Config) { c.RateIPPerMin, c.RateUserPerMin = 30, 1_000_000 })
	limited := false
	for range 80 { // no session at all: an address hammering the sign-in page
		if s2.newClient().do("GET", "/api/v1/version", nil).Status == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("the per-address limit never applied")
	}
}

// One linked person cannot flood Core through a bot, and another person on the same bot is unaffected (SEC-BOT-10).
func TestBotIngestLimits(t *testing.T) {
	s := newStackWith(t, func(c *config.Config) { c.RateIdentityPerMin = 30 })
	key := s.makeBot("m", "example.org")
	noisy, quiet := s.chatter("noisy", key), s.chatter("quiet", key)
	statuses := map[int]int{}
	for i := range 60 {
		ev := map[string]any{"event_id": uuid.NewString(), "kind": "message_created", "sender": noisy.ext, "conversation": noisy.conv, "message_id": uuid.NewString(),
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "parts": []map[string]any{{"type": "text", "text": "spam " + string(rune('a'+i%26))}}}
		statuses[s.botDo(key, "POST", "/bot/v1/events", ev).Status]++
	}
	if statuses[429] == 0 || statuses[200] == 0 {
		t.Fatalf("statuses %v", statuses)
	}
	if out := quiet.send("$q1", 0, text("just one")); out.Result != "created" {
		t.Fatalf("a quiet person on the same bot was refused: %+v", out)
	}
}

// Only so many uploads run at once for one person; the next is refused until one ends (SEC-API-4).
func TestConcurrentUploadsAreCapped(t *testing.T) {
	s := newStackWith(t, func(c *config.Config) { c.MaxConcurrentUpload = 1 })
	u := s.appUser("alice")
	pr, pw := io.Pipe()
	req, _ := http.NewRequest("PUT", s.user.URL+"/api/v1/attachments/"+uuid.NewString(), pr)
	req.ContentLength = 1000
	req.Header.Set("X-Notekeeper-Client", "web")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Filename", "slow.bin")
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range u.c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	done := make(chan error, 1)
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = res.Body.Close()
		}
		done <- err
	}()
	_, _ = pw.Write(bytes.Repeat([]byte("x"), 100)) // the first upload is under way and holds the only slot
	time.Sleep(300 * time.Millisecond)
	if res := u.upload(uuid.NewString(), "second.bin", "application/octet-stream", []byte("data")); res.Status != 429 || res.Code() != "too_many_uploads" {
		t.Fatalf("second upload: %d %s", res.Status, res.Body)
	}
	_, _ = pw.Write(bytes.Repeat([]byte("x"), 900))
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if res := u.upload(uuid.NewString(), "third.bin", "application/octet-stream", []byte("data")); res.Status != 201 {
		t.Fatalf("after the first ended: %d %s", res.Status, res.Body)
	}
}
