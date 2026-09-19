// Command matrix-bot connects Matrix direct messages to Notekeeper through the bot API
// (docs/design/06-matrix-bot.md).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Niboor/notekeeper/bots/matrix/internal/bot"
	"github.com/Niboor/notekeeper/bots/matrix/internal/config"
	"github.com/Niboor/notekeeper/bots/matrix/internal/leader"
	"github.com/Niboor/notekeeper/bots/sdk"
)

// Version is overridden with -ldflags "-X main.Version=...".
var Version = "dev"

func run(args []string) (string, int) {
	if len(args) > 0 && args[0] == "version" {
		return Version, 0
	}
	return "usage: matrix-bot <run|version>", 2
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "run" {
		if err := serve(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	out, code := run(os.Args[1:])
	if code == 0 {
		fmt.Println(out)
	} else {
		fmt.Fprintln(os.Stderr, out)
	}
	os.Exit(code)
}

func serve() error {
	cfg, err := config.Load(nil)
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	core, err := sdk.NewClient(cfg.CoreURL, cfg.BotKey, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return err
	}
	core.OnRetry = func(op string, attempt int, err error) {
		log.Warn("core unavailable; retrying", "operation", op, "attempt", attempt, "error", err)
	}
	b := bot.New(cfg, log, core)

	// Health and metrics come up first so Kubernetes can see "alive, not ready" while we wait for the lock.
	var leading, started atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		last := b.LastSync()
		if !leading.Load() || !started.Load() || last.IsZero() || time.Since(last) > 2*time.Minute || !core.Reachable(r.Context()) {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.Handle("/metrics", promhttp.HandlerFor(b.Registry(), promhttp.HandlerOpts{}))
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("health server failed", "error", err)
		}
	}()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// Only one instance may sync the account (MX-N2). Wait passively for the lock.
	lock, err := leader.Acquire(ctx, cfg.DatabaseURL, cfg.InstanceName, 5*time.Second, func() {
		log.Info("another instance holds the lock; waiting")
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer lock.Close()
	leading.Store(true)
	log.Info("acquired the single-instance lock")

	if err := b.Start(ctx); err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	started.Store(true)

	syncCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go b.Heartbeat(syncCtx)
	go func() {
		// A process that may have lost the lock must not keep syncing.
		select {
		case <-lock.Lost():
			log.Error("lost the single-instance lock; exiting")
			cancel()
			os.Exit(3)
		case <-syncCtx.Done():
		}
	}()
	err = b.Sync(syncCtx)
	if err != nil {
		log.Error("sync ended", "error", err)
	}
	return err
}
