//go:build integration

package server_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The metrics say what the service is doing, they alert on attacks, and they carry no names, no note
// text and no tokens (NFR-O2, SEC-AUD-3, SEC-DATA-1).
func TestMetricsCoverTheServiceAndLeakNothing(t *testing.T) {
	s := newStack(t)
	key := s.makeBot("m", "example.org")
	ch := s.chatter("alicemetrics", key)
	secretText := "the merger will close on friday"
	ch.send("$a", 0, text(secretText))
	ch.send("$b", time.Second, ch.photo("x.png"))
	n := ch.note("", "reminder target note", nil)
	due := time.Now().Add(time.Hour)
	ch.remind(n.ID, due, "")
	s.clockAt(due.Add(time.Minute))
	_, _ = s.svc.Reminders.FireDue(t.Context())
	link := ch.share(n.ID, "1d")
	s.publicDo(link.token(t), "GET", "/api/public/v1/share", nil)
	s.publicDo("A"+strings.Repeat("b", 31), "GET", "/api/public/v1/share", nil)
	s.botDo("nkb.nobody.wrong", "GET", "/bot/v1/outbox", nil)
	s.newClient().do("POST", "/api/v1/auth/login", map[string]any{"username": "alicemetrics", "password": "wrong password here"})

	res, err := http.Get(s.ops.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	out := string(b)
	for _, want := range []string{
		`nk_ingest_events_total{kind="message_created",result="created"}`, `nk_grouping_decisions_total{reason="first"}`, `nk_grouping_decisions_total{reason="media-adjacency"}`,
		`nk_reminders_fired_total{late="false"}`, "nk_reminder_lag_seconds_bucket", `nk_outbox_items{state="queued"}`, "nk_db_pool_connections", "nk_realtime_streams",
		`nk_auth_events_total{event="failed"}`, `nk_bot_requests_rejected_total{reason="bad_key"}`, `nk_share_requests_total{result="ok"}`, `nk_share_requests_total{result="not_found"}`,
		"nk_http_requests_total", `nk_build_info{component="core"`, "nk_outbox_oldest_wait_seconds", "nk_db_pool_acquires_total",
		"nk_db_pool_acquire_wait_seconds_total", "nk_db_pool_blocked_acquires_total", "nk_db_pool_canceled_acquires_total",
		"nk_realtime_streams_refused_total",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing metric %s", want)
		}
	}
	for _, secret := range []string{"alicemetrics", secretText, "merger", link.token(t), "example.org", "nkb.nobody", "x.png"} {
		if strings.Contains(out, secret) {
			t.Errorf("the metrics carry %q", secret)
		}
	}
}
