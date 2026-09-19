package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/gen/botapi"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"log/slog"

	"github.com/Niboor/notekeeper/core/internal/ingest"
	"github.com/Niboor/notekeeper/core/internal/version"
)

type botPrincipalKey struct{}

func botFrom(ctx context.Context) (*bots.Principal, error) {
	p, ok := ctx.Value(botPrincipalKey{}).(*bots.Principal)
	if !ok || p == nil {
		return nil, errUnauthenticated
	}
	return p, nil
}

// botAuth authenticates bot API requests with the bot key and checks the scope each operation
// declares (SEC-BOT-1, SEC-BOT-2). No other credential type is accepted here.
type botAuth struct {
	bots   *bots.Service
	scopes map[string]string
	log    *slog.Logger
}

func (a *botAuth) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pattern := ""
		if rc := chi.RouteContext(r.Context()); rc != nil {
			pattern = rc.RoutePattern()
		}
		scope, declared := a.scopes[opKey(r.Method, pattern)]
		if !declared {
			httpx.NotFound(w, r)
			return
		}
		if scope == "" {
			h.ServeHTTP(w, r)
			return
		}
		token := ""
		if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			token = strings.TrimSpace(t)
		}
		p, err := a.bots.Authenticate(r.Context(), token)
		if err != nil {
			if mapped := mapError(err); mapped != nil && mapped != errUnauthenticated {
				httpx.WriteError(w, r, mapped)
				return
			}
			a.log.Warn("bot authentication failed", "request_id", httpx.RequestIDFrom(r.Context()))
			httpx.WriteError(w, r, errUnauthenticated)
			return
		}
		if scope != "any" && !p.HasScope(scope) {
			httpx.WriteError(w, r, errForbidden)
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), botPrincipalKey{}, p)))
	})
}

type botAPI struct {
	bots   *bots.Service
	ingest *ingest.Service
}

func (b *botAPI) GetBotVersion(context.Context, botapi.GetBotVersionRequestObject) (botapi.GetBotVersionResponseObject, error) {
	return botapi.GetBotVersion200JSONResponse{Version: version.Version}, nil
}

func (b *botAPI) PostEvent(ctx context.Context, req botapi.PostEventRequestObject) (botapi.PostEventResponseObject, error) {
	bot, err := botFrom(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	e := req.Body
	ev := ingest.Event{EventID: e.EventId, Kind: string(e.Kind), Sender: e.Sender, Conversation: e.Conversation,
		MessageID: e.MessageId, Timestamp: e.Timestamp}
	if e.RelatesTo != nil {
		if e.RelatesTo.ReplyTo != nil {
			ev.ReplyTo = *e.RelatesTo.ReplyTo
		}
		if e.RelatesTo.Thread != nil {
			ev.Thread = *e.RelatesTo.Thread
		}
	}
	if e.Parts != nil {
		for _, p := range *e.Parts {
			part := ingest.Part{Type: string(p.Type), Text: deref(p.Text), Filename: deref(p.Filename), MediaType: deref(p.MediaType),
				Reason: deref(p.Reason), Description: deref(p.Description)}
			if p.Size != nil {
				part.Size = *p.Size
			}
			if p.UploadId != nil {
				id := uuid.UUID(*p.UploadId)
				part.UploadID = &id
			}
			ev.Parts = append(ev.Parts, part)
		}
	}
	out, err := b.ingest.Handle(ctx, bot, ev)
	if err != nil {
		return nil, err
	}
	res := botapi.PostEvent200JSONResponse{Result: botapi.EventResultResult(out.Result), Feedback: feedbackOf(out.Feedback)}
	if out.NoteID != nil {
		res.NoteId = out.NoteID
	}
	if out.Code != "" {
		res.Code = &out.Code
	}
	return res, nil
}

func feedbackOf(f ingest.Feedback) botapi.Feedback {
	out := botapi.Feedback{ReplyText: f.ReplyText}
	if f.React != "" {
		out.React = &f.React
	}
	return out
}

func (b *botAPI) PostCommand(ctx context.Context, req botapi.PostCommandRequestObject) (botapi.PostCommandResponseObject, error) {
	bot, err := botFrom(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	c := req.Body
	cmd := ingest.Command{Name: c.Command, Args: deref(c.Args), Sender: c.Sender, Conversation: c.Conversation, ReplyTo: deref(c.ReplyTo)}
	out, err := b.ingest.HandleCommand(ctx, bot, cmd)
	if err != nil {
		return nil, err
	}
	return botapi.PostCommand200JSONResponse{Ok: out.OK, Feedback: feedbackOf(out.Feedback)}, nil
}

func (b *botAPI) GetIdentity(ctx context.Context, req botapi.GetIdentityRequestObject) (botapi.GetIdentityResponseObject, error) {
	bot, err := botFrom(ctx)
	if err != nil {
		return nil, err
	}
	ident, ok, err := b.bots.ResolveIdentity(ctx, bot, req.ExternalUserId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return botapi.GetIdentity200JSONResponse{Linked: false}, nil
	}
	res := botapi.GetIdentity200JSONResponse{Linked: true, LinkedAt: &ident.LinkedAt}
	if ident.BotInstanceID == bot.InstanceID {
		res.Conversation = ident.ConversationID
	}
	return res, nil
}

func (b *botAPI) PostHeartbeat(ctx context.Context, _ botapi.PostHeartbeatRequestObject) (botapi.PostHeartbeatResponseObject, error) {
	bot, err := botFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := b.bots.Heartbeat(ctx, bot.InstanceID); err != nil {
		return nil, err
	}
	return botapi.PostHeartbeat204Response{}, nil
}
