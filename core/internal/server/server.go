// Package server assembles Core's four listeners: user API, bot API, public share API and
// ops (docs/design/README section 2). Each has its own router containing only its own routes.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/blobs"
	"github.com/Niboor/notekeeper/core/internal/board"
	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/config"
	"github.com/Niboor/notekeeper/core/internal/gen/botapi"
	"github.com/Niboor/notekeeper/core/internal/gen/publicapi"
	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"github.com/Niboor/notekeeper/core/internal/ingest"
	"github.com/Niboor/notekeeper/core/internal/notes"
	"github.com/Niboor/notekeeper/core/internal/outbox"
	"github.com/Niboor/notekeeper/core/internal/realtime"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/version"
)

// Deps are the collaborators the handlers need. They grow with each milestone.
type Deps struct {
	Config config.Config
	Log    *slog.Logger
	Ready  Readiness

	Store    *store.Store
	Accounts *accounts.Service
	Bots     *bots.Service
	Notes    *notes.Service
	Board    *board.Service
	Blobs    *blobs.Service
	Ingest   *ingest.Service
	Outbox   *outbox.Service
	Hub      *realtime.Hub
}

// Routers holds the four handlers so that tests can exercise them without listening.
type Routers struct {
	User, Bot, Public, Ops http.Handler
	hub                    *realtime.Hub
}

// userAPI implements the generated user API and the hand-written SSE handler.
type userAPI struct {
	st      *store.Store
	accts   *accounts.Service
	bots    *bots.Service
	notes   *notes.Service
	board   *board.Service
	blobs   *blobs.Service
	hub     *realtime.Hub
	trusted []netip.Prefix
	log     *slog.Logger
}

// NewRouters builds the handlers for the four listeners.
func NewRouters(d Deps) (Routers, error) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	metrics := httpx.NewMetrics(reg)

	base := func(name string, hosts []string, trustRequestID bool) *chi.Mux {
		r := chi.NewRouter()
		r.Use(
			httpx.RequestID(trustRequestID),
			httpx.Recoverer(d.Log),
			httpx.Observe(name, d.Log, metrics),
			httpx.HostCheck(hosts),
			httpx.APIHeaders(),
			httpx.LimitBody(httpx.MaxJSONBody, uploadRoute),
		)
		r.NotFound(httpx.NotFound)
		r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
			httpx.WriteProblem(w, req, http.StatusMethodNotAllowed, "method_not_allowed", "")
		})
		return r
	}
	trusted, err := httpx.ParsePrefixes(d.Config.TrustedProxies)
	if err != nil {
		return Routers{}, fmt.Errorf("NK_TRUSTED_PROXIES: %w", err)
	}

	// ---- user API ----
	userSpec, err := userapi.GetSpec()
	if err != nil {
		return Routers{}, err
	}
	reqs, err := RequirementsFromSpec(userSpec)
	if err != nil {
		return Routers{}, err
	}
	auth := &userAuth{accts: d.Accounts, reqs: reqs, appHosts: d.Config.AppHosts, trusted: trusted}
	ua := &userAPI{st: d.Store, accts: d.Accounts, bots: d.Bots, notes: d.Notes, board: d.Board, blobs: d.Blobs, hub: d.Hub, trusted: trusted, log: d.Log}
	user := base("user", d.Config.AppHosts, false)
	// The authentication middleware sits on a route group, so it runs after routing (it needs the
	// route) but BEFORE the generated code parses parameters and bodies: an unauthenticated caller
	// is refused with 401 whatever its request looks like, never with a validation error.
	user.Group(func(g chi.Router) {
		g.Use(auth.wrap)
		userapi.HandlerWithOptions(
			userapi.NewStrictHandlerWithOptions(ua, []userapi.StrictMiddlewareFunc{stashHTTPUser}, userapi.StrictHTTPServerOptions{
				RequestErrorHandlerFunc: badRequest, ResponseErrorHandlerFunc: errorHandler(d.Log)}),
			userapi.ChiServerOptions{BaseRouter: g, ErrorHandlerFunc: badRequest})
	})
	// Registered after the generated routes so that it replaces the generated placeholder.
	user.Get("/api/v1/events", auth.wrap(http.HandlerFunc(ua.events)).ServeHTTP)

	// ---- bot API ----
	// The bot API is cluster-internal; callers are our own bots, whose request ids we trust
	// so a chat message can be followed end to end (NFR-O1).
	botSpec, err := botapi.GetSpec()
	if err != nil {
		return Routers{}, err
	}
	botScopes, err := BotScopesFromSpec(botSpec)
	if err != nil {
		return Routers{}, err
	}
	bauth := &botAuth{bots: d.Bots, scopes: botScopes, log: d.Log}
	ba := &botAPI{bots: d.Bots, ingest: d.Ingest, blobs: d.Blobs, outbox: d.Outbox, hub: d.Hub, st: d.Store}
	bot := base("bot", nil, true)
	bot.Group(func(g chi.Router) {
		g.Use(bauth.wrap)
		botapi.HandlerWithOptions(
			botapi.NewStrictHandlerWithOptions(ba, []botapi.StrictMiddlewareFunc{stashHTTPBot}, botapi.StrictHTTPServerOptions{
				RequestErrorHandlerFunc: badRequest, ResponseErrorHandlerFunc: errorHandler(d.Log)}),
			botapi.ChiServerOptions{BaseRouter: g, ErrorHandlerFunc: badRequest})
	})

	// ---- public share API ---- (placeholder until milestone M4)
	public := base("public", d.Config.ShareHosts, false)
	publicapi.HandlerFromMux(publicapi.NewStrictHandlerWithOptions(publicHandler{}, nil, publicapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: badRequest, ResponseErrorHandlerFunc: errorHandler(d.Log)}), public)

	return Routers{User: user, Bot: bot, Public: public, Ops: opsRouter(reg, d.Ready), hub: d.Hub}, nil
}

func stashHTTPUser(f userapi.StrictHandlerFunc, _ string) userapi.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, args interface{}) (interface{}, error) {
		return f(httpx.WithHTTP(ctx, w, r), w, r, args)
	}
}

func stashHTTPBot(f botapi.StrictHandlerFunc, _ string) botapi.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, args interface{}) (interface{}, error) {
		return f(httpx.WithHTTP(ctx, w, r), w, r, args)
	}
}

// Listeners returns the four listeners for cfg.
func (r Routers) Listeners(cfg config.Config) []httpx.Listener {
	var onShutdown func()
	if r.hub != nil {
		onShutdown = r.hub.Shutdown
	}
	return []httpx.Listener{
		{Name: "user", Addr: cfg.UserAddr, Handler: r.User, OnShutdown: onShutdown},
		{Name: "bot", Addr: cfg.BotAddr, Handler: r.Bot},
		{Name: "public", Addr: cfg.PublicAddr, Handler: r.Public},
		{Name: "ops", Addr: cfg.OpsAddr, Handler: r.Ops},
	}
}

type publicHandler struct{}

func (publicHandler) GetPublicVersion(context.Context, publicapi.GetPublicVersionRequestObject) (publicapi.GetPublicVersionResponseObject, error) {
	return publicapi.GetPublicVersion200JSONResponse{Version: version.Version}, nil
}

// uploadRoute reports the streaming upload routes, the only ones allowed to exceed the JSON body
// limit; they enforce the attachment size limit and the quota themselves (CORE-A3).
func uploadRoute(r *http.Request) bool {
	return r.Method == http.MethodPut &&
		(strings.HasPrefix(r.URL.Path, "/api/v1/attachments/") || strings.HasPrefix(r.URL.Path, "/bot/v1/uploads/"))
}
