// Command core is the Notekeeper backend: user, bot and public share APIs plus workers
// (docs/design/README.md). Subcommands: serve, migrate, admin, version.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // reminders and profiles use IANA zones; do not depend on the image having them

	"github.com/Niboor/notekeeper/core/internal/app"
	"github.com/Niboor/notekeeper/core/internal/config"
	"github.com/Niboor/notekeeper/core/internal/db"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"github.com/Niboor/notekeeper/core/internal/jobs"
	"github.com/Niboor/notekeeper/core/internal/realtime"
	"github.com/Niboor/notekeeper/core/internal/server"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(ctx)
	case "migrate":
		err = migrate(ctx)
	case "version":
		fmt.Println(version.Version)
	case "admin":
		err = admin(ctx, os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: core <serve|migrate|admin|version>")
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

func serve(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.RequireDatabase(); err != nil {
		return err
	}
	if err := cfg.RequireAuth(); err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	if cfg.MigrateDatabaseURL != "" {
		// Whoever can read this process's environment can read the schema owner's password, and the owner
		// can switch row-level security off. The Secret that holds it belongs to the migration Job alone.
		log.Warn("NK_MIGRATE_DATABASE_URL is set on a serving process; the schema owner's credential belongs to the migration Job only (SEC-OPS-5)")
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	st := store.New(pool)
	st.SetLimits(store.Limits{MaxNotes: cfg.MaxNotesPerUser, MaxTextBytes: cfg.MaxTextBytesPerUser})
	sv, err := app.NewServices(cfg, st, log)
	if err != nil {
		return err
	}
	hub := realtime.NewHub(cfg.DatabaseURL, log)
	go hub.Run(ctx)

	// Background jobs (River). Its tables are created by `core migrate`, never here (no DDL rights).
	workers, err := jobs.New(pool, jobs.Deps{Store: st, Blobs: sv.Blobs, Outbox: sv.Outbox, Shares: sv.Shares, Reminders: sv.Reminders, Accounts: sv.Accounts, Log: log})
	if err != nil {
		return err
	}
	// The jobs run on a context of their own and are stopped explicitly, after the listeners have drained: a
	// signal must not cut running jobs off before the requests they may be waiting for are done. Both share
	// one shutdown budget, counted from the signal, so a stopping pod never needs more than
	// NK_SHUTDOWN_TIMEOUT (and terminationGracePeriodSeconds can be set just above it).
	var signalled atomic.Int64
	context.AfterFunc(ctx, func() { signalled.Store(time.Now().UnixNano()) })
	if err := workers.Start(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	defer func() {
		start := time.Now()
		if at := signalled.Load(); at != 0 {
			start = time.Unix(0, at)
		}
		stopCtx, cancel := context.WithDeadline(context.Background(), start.Add(cfg.ShutdownTimeout))
		defer cancel()
		if err := workers.Stop(stopCtx); err != nil {
			log.Warn("stopping background jobs", "error", err)
		}
	}()

	routers, err := server.NewRouters(server.Deps{
		Config: cfg, Log: log, Store: st,
		Accounts: sv.Accounts, Bots: sv.Bots, Notes: sv.Notes, Board: sv.Board, Blobs: sv.Blobs, Ingest: sv.Ingest, Outbox: sv.Outbox, Shares: sv.Shares, Reminders: sv.Reminders, Export: sv.Export, Hub: hub,
		Ready: func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				log.Warn("not ready: database unreachable", "error", err)
				return err
			}
			current, err := db.MigrationsCurrent(ctx, pool)
			if err != nil {
				log.Warn("not ready: cannot check migrations", "error", err)
				return err
			}
			if !current {
				log.Warn("not ready: migrations pending")
				return fmt.Errorf("migrations pending")
			}
			return nil
		},
	})
	if err != nil {
		return err
	}
	log.Info("starting", "version", version.Version)
	return httpx.Serve(ctx, log, cfg.ShutdownTimeout, routers.Listeners(cfg)...)
}

func migrate(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// The migration Job runs under the schema-owning role and needs no other credential; the serving
	// processes never have DDL rights (docs/design/01-data-model.md section 12).
	if err := cfg.RequireMigrateDatabase(); err != nil {
		return err
	}
	return db.Migrate(ctx, cfg.MigrateDatabaseURL)
}
