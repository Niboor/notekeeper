// Package app builds Core's domain services from the configuration. The binary and the tests
// use the same construction so tests exercise what production runs.
package app

import (
	"log/slog"
	"time"

	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/auth"
	"github.com/Niboor/notekeeper/core/internal/blobs"
	"github.com/Niboor/notekeeper/core/internal/board"
	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/config"
	"github.com/Niboor/notekeeper/core/internal/ingest"
	"github.com/Niboor/notekeeper/core/internal/notes"
	"github.com/Niboor/notekeeper/core/internal/outbox"
	"github.com/Niboor/notekeeper/core/internal/reminders"
	"github.com/Niboor/notekeeper/core/internal/shares"
	"github.com/Niboor/notekeeper/core/internal/store"
)

// Services is the set of domain services built from the configuration.
type Services struct {
	Accounts  *accounts.Service
	Bots      *bots.Service
	Notes     *notes.Service
	Blobs     *blobs.Service
	Board     *board.Service
	Ingest    *ingest.Service
	Outbox    *outbox.Service
	Shares    *shares.Service
	Reminders *reminders.Service
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
	bl := blobs.New(st, blobs.Config{MaxSize: cfg.MaxAttachmentBytes, DefaultQuota: cfg.DefaultQuotaBytes})
	n := notes.New(st, bl)
	return &Services{
		Accounts:  accounts.New(st, keys, hasher, acfg, log),
		Bots:      b,
		Notes:     n,
		Blobs:     bl,
		Board:     board.New(st, n),
		Ingest:    ingest.New(st, b, bl),
		Outbox:    outbox.New(st),
		Reminders: reminders.New(st, reminders.Config{AppURL: cfg.AppURL}),
		Shares:    shares.New(st, n, bl, shares.Config{Enabled: cfg.ShareEnabled, MaxLifetime: cfg.ShareMaxLifetime, MaxActive: cfg.ShareMaxActive}),
	}, nil
}
