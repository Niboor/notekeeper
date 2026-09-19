package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"
)

// Listener is one HTTP listener with its own router (docs/design/README section 2).
type Listener struct {
	Name    string
	Addr    string
	Handler http.Handler
}

// Serve runs all listeners until ctx is cancelled, then shuts them down gracefully within
// timeout. It returns the first listener error, if any.
func Serve(ctx context.Context, log *slog.Logger, timeout time.Duration, listeners ...Listener) error {
	g, gctx := errgroup.WithContext(ctx)
	for _, l := range listeners {
		srv := &http.Server{
			Addr:              l.Addr,
			Handler:           l.Handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			// No global read/write timeouts: streaming routes (uploads, downloads, SSE,
			// long-polls) set per-request deadlines instead (docs/design/02 section 4).
		}
		g.Go(func() error {
			log.Info("listening", "listener", l.Name, "addr", l.Addr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		})
		g.Go(func() error {
			<-gctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			return srv.Shutdown(sctx)
		})
	}
	return g.Wait()
}
