// Package server assembles Core's four listeners: user API, bot API, public share API and
// ops (docs/design/README section 2). Each has its own router containing only its own routes.
package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"notekeeper/core/internal/config"
	"notekeeper/core/internal/gen/botapi"
	"notekeeper/core/internal/gen/publicapi"
	"notekeeper/core/internal/gen/userapi"
	"notekeeper/core/internal/httpx"
	"notekeeper/core/internal/version"
)

// Deps are the collaborators the handlers need. They grow with each milestone.
type Deps struct {
	Config config.Config
	Log    *slog.Logger
	Ready  Readiness
}

// Routers holds the four handlers so that tests can exercise them without listening.
type Routers struct {
	User, Bot, Public, Ops http.Handler
}

// NewRouters builds the handlers for the four listeners.
func NewRouters(d Deps) Routers {
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
		)
		r.NotFound(httpx.NotFound)
		r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
			httpx.WriteProblem(w, req, http.StatusMethodNotAllowed, "method_not_allowed", "")
		})
		return r
	}

	user := base("user", d.Config.AppHosts, false)
	userapi.HandlerFromMux(userapi.NewStrictHandlerWithOptions(userHandler{}, nil, strictErrors()), user)

	// The bot API is cluster-internal; callers are our own bots, whose request ids we trust
	// so a chat message can be followed end to end (NFR-O1).
	bot := base("bot", nil, true)
	botapi.HandlerFromMux(botapi.NewStrictHandlerWithOptions(botHandler{}, nil, botStrictErrors()), bot)

	public := base("public", d.Config.ShareHosts, false)
	publicapi.HandlerFromMux(publicapi.NewStrictHandlerWithOptions(publicHandler{}, nil, publicStrictErrors()), public)

	return Routers{User: user, Bot: bot, Public: public, Ops: opsRouter(reg, d.Ready)}
}

// Listeners returns the four listeners for cfg.
func (r Routers) Listeners(cfg config.Config) []httpx.Listener {
	return []httpx.Listener{
		{Name: "user", Addr: cfg.UserAddr, Handler: r.User},
		{Name: "bot", Addr: cfg.BotAddr, Handler: r.Bot},
		{Name: "public", Addr: cfg.PublicAddr, Handler: r.Public},
		{Name: "ops", Addr: cfg.OpsAddr, Handler: r.Ops},
	}
}

// ---- handlers: placeholders proving the generated pipeline; replaced from M1 on -------------

type userHandler struct{}

func (userHandler) GetVersion(context.Context, userapi.GetVersionRequestObject) (userapi.GetVersionResponseObject, error) {
	return userapi.GetVersion200JSONResponse{Version: version.Version}, nil
}

type botHandler struct{}

func (botHandler) GetBotVersion(context.Context, botapi.GetBotVersionRequestObject) (botapi.GetBotVersionResponseObject, error) {
	return botapi.GetBotVersion200JSONResponse{Version: version.Version}, nil
}

type publicHandler struct{}

func (publicHandler) GetPublicVersion(context.Context, publicapi.GetPublicVersionRequestObject) (publicapi.GetPublicVersionResponseObject, error) {
	return publicapi.GetPublicVersion200JSONResponse{Version: version.Version}, nil
}

// Strict handlers report request and response errors as generic problem+json (SEC-API-5).
func strictErrors() userapi.StrictHTTPServerOptions {
	return userapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: badRequest, ResponseErrorHandlerFunc: internalError}
}

func botStrictErrors() botapi.StrictHTTPServerOptions {
	return botapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: badRequest, ResponseErrorHandlerFunc: internalError}
}

func publicStrictErrors() publicapi.StrictHTTPServerOptions {
	return publicapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: badRequest, ResponseErrorHandlerFunc: internalError}
}

func badRequest(w http.ResponseWriter, r *http.Request, _ error) {
	httpx.WriteProblem(w, r, http.StatusBadRequest, "bad_request", "")
}

func internalError(w http.ResponseWriter, r *http.Request, _ error) {
	httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "")
}
