package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"github.com/Niboor/notekeeper/core/internal/realtime"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

const (
	sseHeartbeat = 25 * time.Second
	sseSafetyNet = 30 * time.Second // poll the change counter in case a notification was missed
	sseBatch     = 200
)

// StreamEvents is the generated placeholder for the event stream; the route is re-registered
// with the hand-written handler below, which needs to flush and hold the connection.
func (u *userAPI) StreamEvents(context.Context, userapi.StreamEventsRequestObject) (userapi.StreamEventsResponseObject, error) {
	return nil, errNotFound
}

// events serves GET /api/v1/events (docs/design/05-realtime-and-jobs.md section 2): a stream of
// change notifications for the caller's own data. Nothing else is subscribable, so reading
// another user's changes is structurally impossible (SEC-ISO-5).
func (u *userAPI) events(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, errUnauthenticated)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "")
		return
	}
	stream, err := u.hub.Subscribe(p.UserID, p.SessionID)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(http.StatusTooManyRequests, "too_many_streams"))
		return
	}
	defer u.hub.Unsubscribe(stream)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // do not let nginx buffer the stream
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	var last int64
	resync := false
	if id, ok := realtime.ParseSeq(r.Header.Get("Last-Event-ID")); ok {
		last = id
	} else {
		// Fresh connection: say where we are; the client refetches its views on "hello", which
		// closes the gap between its initial fetch and this subscription.
		cur, err := u.st.Q().CurrentChangeSeq(ctx, p.UserID)
		if err != nil {
			return
		}
		last = cur
		writeEvent(w, "", "hello", fmt.Sprintf(`{"seq":%d}`, cur))
		fl.Flush()
	}
	_ = resync

	// The stream ends when the access token that opened it expires; EventSource reconnects with
	// the renewed cookie, so this is invisible to the user (SEC-ISO-5).
	expiry := time.NewTimer(time.Until(p.TokenExpiry))
	defer expiry.Stop()
	beat := time.NewTicker(sseHeartbeat)
	defer beat.Stop()
	safety := time.NewTicker(sseSafetyNet)
	defer safety.Stop()

	drain := func() bool {
		for {
			var rows []dbq.ListChangesRow
			var oldest int64
			err := u.st.InUserRead(ctx, p.UserID, func(q *dbq.Queries) error {
				var err error
				if oldest, err = q.OldestChangeSeq(ctx, p.UserID); err != nil {
					return err
				}
				rows, err = q.ListChanges(ctx, dbq.ListChangesParams{UserID: p.UserID, Seq: last, Limit: sseBatch})
				return err
			})
			if err != nil {
				return false
			}
			if oldest > 0 && oldest > last+1 {
				writeEvent(w, "", "resync", "{}") // retention passed us by: refetch everything
				fl.Flush()
				cur, err := u.st.Q().CurrentChangeSeq(ctx, p.UserID)
				if err != nil {
					return false
				}
				last = cur
				return true
			}
			for _, c := range rows {
				data, _ := json.Marshal(map[string]any{"entity_type": c.EntityType, "entity_id": c.EntityID, "op": c.Op, "version": c.Version})
				writeEvent(w, strconv.FormatInt(c.Seq, 10), "change", string(data))
				last = c.Seq
			}
			fl.Flush()
			if len(rows) < sseBatch {
				return true
			}
		}
	}
	if !drain() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-stream.Done():
			if stream.Reconnect {
				writeEvent(w, "", "reconnect", "{}")
				fl.Flush()
			}
			return
		case <-expiry.C:
			return
		case <-stream.Wake:
			if !drain() {
				return
			}
		case <-safety.C:
			if !drain() {
				return
			}
		case <-beat.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, id, event, data string) {
	if id != "" {
		_, _ = fmt.Fprintf(w, "id: %s\n", id)
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}
