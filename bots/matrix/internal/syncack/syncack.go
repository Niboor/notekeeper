// Package syncack makes mautrix-go commit its sync token only after a sync response has been
// fully processed.
//
// mautrix-go saves the next-batch token *before* it dispatches the response's events
// (client.go, SyncWithContext: "Save the token now before processing it"). A crash, or a Core
// outage that makes a handler give up, would therefore skip those events for good, which
// breaks the "never lose a message" requirement (NFR-R1, BOT-B2). This package defers the
// save: the Store swallows SaveNextBatch and remembers the token, and the Syncer commits it
// once ProcessResponse has returned without error.
//
// See docs/design/06-matrix-bot.md section 3.
package syncack

import (
	"context"
	"sync"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// Store wraps a mautrix.SyncStore and defers SaveNextBatch until Commit.
type Store struct {
	mautrix.SyncStore

	mu      sync.Mutex
	pending map[id.UserID]string
}

// NewStore wraps inner (normally the crypto store, which keeps the token in PostgreSQL). The
// inner store may be set later through the embedded SyncStore field: the crypto helper only
// installs its store during Init, after the syncer has been created.
func NewStore(inner mautrix.SyncStore) *Store {
	return &Store{SyncStore: inner, pending: map[id.UserID]string{}}
}

// SaveNextBatch remembers the token instead of persisting it.
func (s *Store) SaveNextBatch(_ context.Context, user id.UserID, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[user] = token
	return nil
}

// Commit persists the remembered token for user, if any.
func (s *Store) Commit(ctx context.Context, user id.UserID) error {
	s.mu.Lock()
	token, ok := s.pending[user]
	delete(s.pending, user)
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return s.SyncStore.SaveNextBatch(ctx, user, token)
}

// Syncer wraps mautrix.DefaultSyncer and commits the sync token after a response has been
// processed successfully. If processing fails (a handler panicked, which ProcessResponse turns
// into an error), the token is not committed and the batch is replayed after a restart; Core
// deduplicates replays by event id (BOT-7).
type Syncer struct {
	*mautrix.DefaultSyncer
	store  *Store
	userID func() id.UserID
}

// NewSyncer returns a syncer that commits through store. userID is called lazily because the
// client only knows its user id after logging in.
func NewSyncer(store *Store, userID func() id.UserID) *Syncer {
	return &Syncer{DefaultSyncer: mautrix.NewDefaultSyncer(), store: store, userID: userID}
}

// ProcessResponse dispatches the response, then commits the token.
func (s *Syncer) ProcessResponse(ctx context.Context, res *mautrix.RespSync, since string) error {
	if err := s.DefaultSyncer.ProcessResponse(ctx, res, since); err != nil {
		return err
	}
	return s.store.Commit(ctx, s.userID())
}
