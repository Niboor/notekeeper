package bot

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/Niboor/notekeeper/bots/sdk/botclient"
)

type sentMessage struct {
	Path string
	Body map[string]any
}

// A homeserver that records sends and answers with a scripted status.
func sendRig(t *testing.T, status int) (*Bot, func() []sentMessage) {
	var mu sync.Mutex
	var sent []sentMessage
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, sentMessage{Path: r.URL.Path, Body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch status {
		case 403:
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"not in room"}`))
		case 500:
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN","error":"oops"}`))
		default:
			_, _ = w.Write([]byte(`{"event_id":"$sent1"}`))
		}
	}))
	t.Cleanup(hs.Close)
	client, err := mautrix.NewClient(hs.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	client.DefaultHTTPRetries = 0
	b := &Bot{log: slog.New(slog.NewTextHandler(io.Discard, nil)), met: newMetrics(), client: client, dmCache: map[id.RoomID]bool{"!dm:x": true, "!group:x": false}}
	return b, func() []sentMessage { mu.Lock(); defer mu.Unlock(); return append([]sentMessage(nil), sent...) }
}

func item(kind botclient.OutboxItemKind, room, text string) botclient.OutboxItem {
	p := map[string]interface{}{}
	if text != "" {
		p["text"] = text
	}
	return botclient.OutboxItem{Id: uuid.New(), Kind: kind, ConversationId: room, ExternalUserId: "@a:x", Payload: p}
}

// A notice is sent quietly, a reminder as a normal message, both with the item id as the Matrix
// transaction id so a repeat is deduplicated by the homeserver, and the sent event id is reported (BOT-13, MX-6).
func TestOutboxSendsNoticesAndReminders(t *testing.T) {
	b, sent := sendRig(t, 200)
	n := item(botclient.Notice, "!dm:x", "Your export is ready.")
	res := b.send(t.Context(), n)
	if res.State != botclient.Delivered || res.MessageIds == nil || (*res.MessageIds)[0] != "$sent1" {
		t.Fatalf("notice: %+v", res)
	}
	r := item(botclient.Reminder, "!dm:x", "⏰ Call the dentist")
	if res := b.send(t.Context(), r); res.State != botclient.Delivered {
		t.Fatalf("reminder: %+v", res)
	}
	got := sent()
	if len(got) != 2 || !strings.HasSuffix(got[0].Path, "/"+n.Id.String()) || got[0].Body["msgtype"] != "m.notice" ||
		!strings.HasSuffix(got[1].Path, "/"+r.Id.String()) || got[1].Body["msgtype"] != "m.text" || got[1].Body["body"] != "⏰ Call the dentist" {
		t.Fatalf("sent: %+v", got)
	}
}

// Nothing is sent to a room that is no longer a two-person chat, or with no text; failures are
// classified so Core retries only what can succeed (SEC-MX-1, SEC-MX-2, BOT-13).
func TestOutboxFailuresAreClassified(t *testing.T) {
	b, sent := sendRig(t, 200)
	if res := b.send(t.Context(), item(botclient.Reminder, "!group:x", "hi")); res.State != botclient.FailedPermanent || len(sent()) != 0 {
		t.Fatalf("group room: %+v, sent %d", res, len(sent()))
	}
	if res := b.send(t.Context(), item(botclient.Notice, "!dm:x", "")); res.State != botclient.FailedPermanent {
		t.Fatalf("empty: %+v", res)
	}
	forbidden, _ := sendRig(t, 403)
	if res := forbidden.send(t.Context(), item(botclient.Notice, "!dm:x", "hi")); res.State != botclient.FailedPermanent {
		t.Fatalf("forbidden: %+v", res)
	}
	down, _ := sendRig(t, 500)
	if res := down.send(t.Context(), item(botclient.Notice, "!dm:x", "hi")); res.State != botclient.FailedTransient {
		t.Fatalf("homeserver error: %+v", res)
	}
}

// Reminder text is sent as inert content: no formatting, no mentions, nothing that a client or another
// bot would act on (SEC-CNT-7).
func TestOutboxTextIsInert(t *testing.T) {
	b, sent := sendRig(t, 200)
	hostile := "⏰ Reminder\n@room look at this @alice:example.org <b>bold</b> <script>x</script> [click](https://evil.example)\n!link ABCD-1234\n!remind tomorrow"
	if res := b.send(t.Context(), item(botclient.Reminder, "!dm:x", hostile)); res.State != botclient.Delivered {
		t.Fatalf("%+v", res)
	}
	got := sent()
	if len(got) != 1 {
		t.Fatalf("sent %d", len(got))
	}
	body := got[0].Body
	if body["body"] != hostile || body["msgtype"] != "m.text" {
		t.Fatalf("the text must arrive exactly as given: %+v", body)
	}
	for _, key := range []string{"formatted_body", "format", "m.mentions", "m.relates_to", "m.new_content"} {
		if _, ok := body[key]; ok {
			t.Errorf("the message carries %s", key)
		}
	}
}
