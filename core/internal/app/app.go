// Package app builds Core's domain services from the configuration. The binary and the tests
// use the same construction so tests exercise what production runs.
package app

import (
	"log/slog"
	"time"

	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/auth"
	"github.com/Niboor/notekeeper/core/internal/board"
	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/config"
	"github.com/Niboor/notekeeper/core/internal/ingest"
	"github.com/Niboor/notekeeper/core/internal/notes"
	"github.com/Niboor/notekeeper/core/internal/store"
)

// Services is the set of domain services built from the configuration.
type Services struct {
	Accounts *accounts.Service
	Bots     *bots.Service
	Notes    *notes.Service
	Board    *board.Service
	Ingest   *ingest.Service
}

// NewServices constructs the services.
func NewServices(cfg config.Config, st *store.Store, log *slog.Logger) (*Services, error) {
	keys, err := auth.ParseKeyring(cfg.TokenKeys)
	if err != nil {
		return nil, err
	}
	hasher, err := auth.NewHasher(auth.Params{Memory: cfg.Argon2MemoryKiB, Time: cfg.Argon2Iterations, Threads: cfg.Argon2Parallelism}, cfg.Argon2Concurrency)
	if err != nil {
		return nil, err
	}
	acfg := accounts.Config{
		AccessTTL: cfg.AccessTokenTTL, IdleLifetime: cfg.SessionIdleLifetime, AbsoluteLifetime: cfg.SessionAbsoluteLifetime,
		SessionOnly: 12 * time.Hour, RefreshGrace: cfg.RefreshGrace, ActivationTTL: cfg.ActivationTTL,
	}
	b := bots.New(st)
	n := notes.New(st)
	return &Services{
		Accounts: accounts.New(st, keys, hasher, acfg, log),
		Bots:     b,
		Notes:    n,
		Board:    board.New(st, n),
		Ingest:   ingest.New(st, b),
	}, nil
}
