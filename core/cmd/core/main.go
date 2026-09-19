// Command core is the Notekeeper backend: user, bot and public share APIs plus workers
// (docs/design/README.md). Subcommands: serve, migrate, admin, version.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Niboor/notekeeper/core/internal/config"
	"github.com/Niboor/notekeeper/core/internal/db"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"github.com/Niboor/notekeeper/core/internal/server"
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
		err = fmt.Errorf("admin commands arrive with milestone M1")
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
	log := newLogger(cfg.LogLevel)
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	routers := server.NewRouters(server.Deps{
		Config: cfg,
		Log:    log,
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
	log.Info("starting", "version", version.Version)
	return httpx.Serve(ctx, log, cfg.ShutdownTimeout, routers.Listeners(cfg)...)
}

func migrate(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.RequireDatabase(); err != nil {
		return err
	}
	return db.Migrate(ctx, cfg.DatabaseURL)
}
