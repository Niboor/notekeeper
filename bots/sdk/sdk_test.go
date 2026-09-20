package sdk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Niboor/notekeeper/bots/sdk/botclient"
)

func TestClientAuthenticatesAndForwardsRequestID(t *testing.T) {
	var gotAuth, gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotID = r.Header.Get("Authorization"), r.Header.Get("X-Request-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"test"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "nkb.abc.secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.GetBotVersionWithResponse(WithRequestID(context.Background(), "req-1"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.JSON200 == nil || resp.JSON200.Version != "test" {
		t.Fatalf("unexpected response: %+v", resp.JSON200)
	}
	if gotAuth != "Bearer nkb.abc.secret" || gotID != "req-1" {
		t.Fatalf("headers: auth=%q id=%q", gotAuth, gotID)
	}
}

func fastClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := NewClient(srv.URL, "nkb.abc.secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.Backoff = func(int) time.Duration { return time.Millisecond }
	return c
}

func TestPostEventRetriesTransientFailuresUntilCoreAnswers(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusBadGateway) // ingress hiccup
		case 2:
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":"created","feedback":{"react":"ok"}}`))
		}
	}))
	defer srv.Close()
	c := fastClient(t, srv)
	var retries int
	c.OnRetry = func(string, int, error) { retries++ }

	res, err := c.PostEvent(context.Background(), botclient.Event{EventId: "$1"})
	if err != nil || res == nil || res.Result != botclient.Created {
		t.Fatalf("result %+v err %v", res, err)
	}
	if calls.Load() != 3 || retries != 2 {
		t.Fatalf("calls=%d retries=%d", calls.Load(), retries)
	}
}

// A request Core fails on every time (a poison event) must not stall the bot for ever, but a passing
// 500 is still retried (CR-003, NFR-R1).
func TestRepeatedServerErrorsBecomePermanent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := fastClient(t, srv)
	c.MaxServerErrors = 4
	_, err := c.PostEvent(context.Background(), botclient.Event{EventId: "$1"})
	var perm *PermanentError
	if !errors.As(err, &perm) || perm.Status != 500 || calls.Load() != 4 {
		t.Fatalf("err = %v after %d calls", err, calls.Load())
	}
}

func TestPassingServerErrorIsRetriedAndOutagesNeverGiveUp(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n := calls.Add(1); {
		case n <= 2:
			w.WriteHeader(http.StatusInternalServerError)
		case n <= 30:
			w.WriteHeader(http.StatusServiceUnavailable) // an outage: no limit applies
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":"created"}`))
		}
	}))
	defer srv.Close()
	c := fastClient(t, srv)
	c.MaxServerErrors = 3
	res, err := c.PostEvent(context.Background(), botclient.Event{EventId: "$1"})
	if err != nil || res == nil || calls.Load() != 31 {
		t.Fatalf("res %+v err %v calls %d", res, err, calls.Load())
	}
}

func TestPostEventRetriesConnectionErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens any more: Core is down
	c, _ := NewClient(url, "k", &http.Client{Timeout: 200 * time.Millisecond})
	c.Backoff = func(int) time.Duration { return time.Millisecond }
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.PostEvent(ctx, botclient.Event{EventId: "$1"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a Core outage must stall the caller until its context ends, got %v", err)
	}
}

func TestPermanentFailuresAreNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,"code":"invalid_event"}`))
	}))
	defer srv.Close()
	_, err := fastClient(t, srv).PostEvent(context.Background(), botclient.Event{EventId: "$1"})
	var perm *PermanentError
	if !errors.As(err, &perm) || perm.Status != 400 || perm.Code != "invalid_event" {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("a permanent failure was retried %d times", calls.Load())
	}
}

func TestDefaultBackoffGrowsAndCaps(t *testing.T) {
	c := &Client{}
	prev := time.Duration(0)
	for a := 1; a <= 12; a++ {
		d := c.backoff(a)
		if d < prev || d > 30*time.Second {
			t.Fatalf("attempt %d: %v after %v", a, d, prev)
		}
		prev = d
	}
	if prev != 30*time.Second {
		t.Fatalf("cap: %v", prev)
	}
}
