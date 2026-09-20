// Package realtime delivers change notifications to open event streams (SSE). Every write
// transaction announces itself with pg_notify inside the transaction; PostgreSQL delivers it on
// commit. Each replica holds one dedicated LISTEN connection outside the pool (so the design
// stays correct if a pooler is ever introduced) and wakes the streams of the affected user
// (docs/design/05-realtime-and-jobs.md sections 1 and 2).
package realtime

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/store"
)

// MaxStreamsPerUser bounds the open streams of one user (SEC-API-4).
const MaxStreamsPerUser = 10

// ErrTooManyStreams is returned when a user already has MaxStreamsPerUser streams open.
var ErrTooManyStreams = errors.New("too many open streams")

// Stream is one subscriber. Wake fires (coalesced) when the user has new changes; Done is
// closed when the stream must end (session revoked, server shutting down).
type Stream struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
	Wake      chan struct{}
	done      chan struct{}
	once      sync.Once
	// Reconnect is true when the stream ends because the server is going away, so the client
	// should reconnect at once rather than treat it as a failure.
	Reconnect bool
}

// Done is closed when the stream must end.
func (s *Stream) Done() <-chan struct{} { return s.done }

func (s *Stream) close(reconnect bool) {
	s.once.Do(func() {
		s.Reconnect = reconnect
		close(s.done)
	})
}

func (s *Stream) wake() {
	select {
	case s.Wake <- struct{}{}:
	default: // already pending: the reader will see everything at once
	}
}

// Hub connects PostgreSQL notifications to streams.
type Hub struct {
	url string
	log *slog.Logger

	mu      sync.Mutex
	streams map[uuid.UUID]map[*Stream]struct{}
	closing bool
	// outbox waiters: bot instance -> channels woken when an item for it is queued.
	outbox map[uuid.UUID]map[chan struct{}]struct{}
}

// NewHub creates a hub that listens using the given connection string (the same database as
// the pool, but its own connection).
func NewHub(url string, log *slog.Logger) *Hub {
	return &Hub{url: url, log: log, streams: map[uuid.UUID]map[*Stream]struct{}{}, outbox: map[uuid.UUID]map[chan struct{}]struct{}{}}
}

// Subscribe registers a stream for a user.
func (h *Hub) Subscribe(user, session uuid.UUID) (*Stream, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.streams[user]) >= MaxStreamsPerUser {
		return nil, ErrTooManyStreams
	}
	s := &Stream{UserID: user, SessionID: session, Wake: make(chan struct{}, 1), done: make(chan struct{})}
	if h.closing {
		s.close(true)
	}
	if h.streams[user] == nil {
		h.streams[user] = map[*Stream]struct{}{}
	}
	h.streams[user][s] = struct{}{}
	return s, nil
}

// Unsubscribe removes a stream.
func (h *Hub) Unsubscribe(s *Stream) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.streams[s.UserID], s)
	if len(h.streams[s.UserID]) == 0 {
		delete(h.streams, s.UserID)
	}
}

// Shutdown ends every stream with a reconnect hint and refuses new ones.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closing = true
	for _, set := range h.streams {
		for s := range set {
			s.close(true)
		}
	}
}

// Count returns the number of open streams (metrics).
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, set := range h.streams {
		n += len(set)
	}
	return n
}

// WaitOutbox returns a channel that receives when an outbox item for the bot instance is queued,
// and a function to stop waiting. It backs the bot's long poll; a periodic re-check covers a
// missed notification.
func (h *Hub) WaitOutbox(instance uuid.UUID) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.outbox[instance] == nil {
		h.outbox[instance] = map[chan struct{}]struct{}{}
	}
	h.outbox[instance][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.outbox[instance], ch)
		if len(h.outbox[instance]) == 0 {
			delete(h.outbox, instance)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) wakeOutbox(instance uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.outbox[instance] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *Hub) wakeUser(user uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.streams[user] {
		s.wake()
	}
}

func (h *Hub) wakeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.streams {
		for s := range set {
			s.wake()
		}
	}
}

func (h *Hub) revoke(payload string) {
	kind, id, ok := strings.Cut(payload, ":")
	uid, err := uuid.Parse(id)
	if !ok || err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch kind {
	case "s": // one session
		for _, set := range h.streams {
			for s := range set {
				if s.SessionID == uid {
					s.close(false)
				}
			}
		}
	case "u": // every session of a user
		for s := range h.streams[uid] {
			s.close(false)
		}
	}
}

// Run listens until ctx ends, reconnecting with backoff. Notifications sent while the
// connection was down are lost, so after every (re)connect all streams are woken to catch up
// from the change log.
func (h *Hub) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := h.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		h.log.Warn("change listener lost its connection", "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (h *Hub) listen(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, h.url)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	for _, ch := range []string{store.ChannelChanges, store.ChannelSessions, store.ChannelOutbox} {
		if _, err := conn.Exec(ctx, "listen "+ch); err != nil {
			return err
		}
	}
	h.wakeAll() // catch up on anything missed while we were not listening
	h.mu.Lock()
	for _, set := range h.outbox {
		for ch := range set {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
	h.mu.Unlock()
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		switch n.Channel {
		case store.ChannelChanges:
			userStr, _, _ := strings.Cut(n.Payload, ":")
			if uid, err := uuid.Parse(userStr); err == nil {
				h.wakeUser(uid)
			}
		case store.ChannelSessions:
			h.revoke(n.Payload)
		case store.ChannelOutbox:
			if id, err := uuid.Parse(n.Payload); err == nil {
				h.wakeOutbox(id)
			}
		}
	}
}

// ParseSeq parses a Last-Event-ID value.
func ParseSeq(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n >= 0
}
