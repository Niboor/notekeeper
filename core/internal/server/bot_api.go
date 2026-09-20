package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/blobs"
	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/gen/botapi"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"github.com/Niboor/notekeeper/core/internal/obs"
	"github.com/Niboor/notekeeper/core/internal/outbox"
	"github.com/Niboor/notekeeper/core/internal/realtime"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
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
				obs.BotRejected.WithLabelValues("refused").Inc()
				httpx.WriteError(w, r, mapped)
				return
			}
			obs.BotRejected.WithLabelValues("bad_key").Inc()
			a.log.Warn("bot authentication failed", "request_id", httpx.RequestIDFrom(r.Context()))
			httpx.WriteError(w, r, errUnauthenticated)
			return
		}
		if scope != "any" && !p.HasScope(scope) {
			obs.BotRejected.WithLabelValues("wrong_scope").Inc()
			httpx.WriteError(w, r, errForbidden)
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), botPrincipalKey{}, p)))
	})
}

type botAPI struct {
	limits *limits
	bots   *bots.Service
	ingest *ingest.Service
	blobs  *blobs.Service
	outbox *outbox.Service
	hub    *realtime.Hub
	st     *store.Store
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
				id := *p.UploadId
				part.UploadID = &id
			}
			ev.Parts = append(ev.Parts, part)
		}
	}
	// One linked person cannot flood Core through a bot, whatever the instance's own allowance (SEC-BOT-10).
	if ok, retry := b.limits.identityAllowed(bot.InstanceID.String() + "/" + ev.Sender); !ok {
		return nil, &httpx.Error{Status: http.StatusTooManyRequests, Code: "rate_limited", RetryAfter: retry}
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
	cmd.MessageID = deref(c.MessageId)
	if c.Timestamp != nil {
		cmd.Timestamp = *c.Timestamp
	}
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
	// Looking up who is linked is limited on its own, so it cannot be used to sweep a homeserver's users, and every
	// lookup is audited without saying whom it asked about (SEC-BOT-3).
	if ok, retry := b.limits.identityAllowed("lookup/" + bot.InstanceID.String()); !ok {
		return nil, &httpx.Error{Status: http.StatusTooManyRequests, Code: "rate_limited", RetryAfter: retry}
	}
	ident, ok, err := b.bots.ResolveIdentity(ctx, bot, req.ExternalUserId)
	if err != nil {
		return nil, err
	}
	_ = store.Audit(ctx, b.st.Q(), store.AuditEntry{ActorKind: "bot", ActorID: &bot.InstanceID, Action: "bot.identity_lookup", Detail: map[string]any{"linked": ok}})
	if !ok {
		return botapi.GetIdentity200JSONResponse{Linked: false}, nil
	}
	return botapi.GetIdentity200JSONResponse{Linked: true, LinkedAt: &ident.LinkedAt, Conversation: ident.ConversationID}, nil
}

func (b *botAPI) PostHeartbeat(ctx context.Context, req botapi.PostHeartbeatRequestObject) (botapi.PostHeartbeatResponseObject, error) {
	bot, err := botFrom(ctx)
	if err != nil {
		return nil, err
	}
	address := ""
	if req.Body != nil && req.Body.Address != nil {
		address = *req.Body.Address
	}
	if err := b.bots.Heartbeat(ctx, bot.InstanceID, bot.IdentityDomain, address); err != nil {
		return nil, err
	}
	return botapi.PostHeartbeat204Response{}, nil
}

// PutUpload stores an attachment for a linked identity: the same path, limits and quota as a user
// upload (BOT-6, CORE-A3). The file belongs to the identity's user and stays unlinked until an
// event references it.
func (b *botAPI) PutUpload(ctx context.Context, req botapi.PutUploadRequestObject) (botapi.PutUploadResponseObject, error) {
	bot, err := botFrom(ctx)
	if err != nil {
		return nil, err
	}
	w, r := httpx.HTTPFrom(ctx)
	if r.ContentLength < 0 {
		return nil, httpx.NewError(http.StatusLengthRequired, "length_required")
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	ident, ok, err := b.bots.ResolveIdentity(ctx, bot, req.Params.XExternalUser)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, httpx.NewError(http.StatusNotFound, "identity_unlinked")
	}
	name, err := url.PathUnescape(req.Params.XFilename)
	if err != nil {
		return nil, errBadRequest.WithDetail("X-Filename is not valid percent-encoding")
	}
	mediaType := ""
	if req.Params.XMediaType != nil {
		mediaType = *req.Params.XMediaType
	}
	ctx, cancel := context.WithTimeout(ctx, uploadDeadline)
	defer cancel()
	a, err := b.blobs.Upload(ctx, ident.UserID, req.Id, name, mediaType, r.ContentLength, idleBody(w, req.Body))
	if err != nil {
		return nil, mapBlobError(err)
	}
	return botapi.PutUpload201JSONResponse{Id: a.ID, Filename: a.Filename, MediaType: a.MediaType, Size: a.Size}, nil
}

// ClaimOutbox long-polls the outbox: it returns as soon as there is something for this bot instance,
// or an empty list after `wait` seconds (docs/design/05 section 4.2).
func (b *botAPI) ClaimOutbox(ctx context.Context, req botapi.ClaimOutboxRequestObject) (botapi.ClaimOutboxResponseObject, error) {
	bot, err := botFrom(ctx)
	if err != nil {
		return nil, err
	}
	wait, limit := 0, 10
	if req.Params.Wait != nil {
		wait = max(0, min(*req.Params.Wait, 25))
	}
	if req.Params.Limit != nil {
		limit = clampParam(*req.Params.Limit, limit, 50) // the spec says 1 to 50; the generated code does not check
	}
	wake, stop := b.hub.WaitOutbox(bot.InstanceID)
	defer stop()
	deadline := time.NewTimer(time.Duration(wait) * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Second) // the notification is an optimisation; polling is the safety net
	defer poll.Stop()
	for {
		items, err := b.outbox.Claim(ctx, bot.InstanceID, limit)
		if err != nil {
			return nil, err
		}
		if len(items) > 0 || wait == 0 {
			out := botapi.ClaimOutbox200JSONResponse{Items: make([]botapi.OutboxItem, len(items))}
			for i, it := range items {
				out.Items[i] = botapi.OutboxItem{Id: it.ID, Kind: botapi.OutboxItemKind(it.Kind), ExternalUserId: it.ExternalUserID,
					ConversationId: it.ConversationID, Payload: it.Payload, Attempts: int(it.Attempts), DueAt: it.DueAt}
			}
			return out, nil
		}
		select {
		case <-ctx.Done():
			return botapi.ClaimOutbox200JSONResponse{Items: []botapi.OutboxItem{}}, nil
		case <-deadline.C:
			return botapi.ClaimOutbox200JSONResponse{Items: []botapi.OutboxItem{}}, nil
		case <-wake:
		case <-poll.C:
		}
	}
}

func (b *botAPI) PostOutboxResult(ctx context.Context, req botapi.PostOutboxResultRequestObject) (botapi.PostOutboxResultResponseObject, error) {
	bot, err := botFrom(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	r := outbox.Result{State: string(req.Body.State)}
	if req.Body.Reason != nil {
		r.Reason = *req.Body.Reason
	}
	if req.Body.MessageIds != nil {
		r.MessageIDs = *req.Body.MessageIds
	}
	if err := b.outbox.Report(ctx, bot.InstanceID, req.Id, r); err != nil {
		if errors.Is(err, outbox.ErrInvalid) {
			return nil, errBadRequest
		}
		return nil, err
	}
	return botapi.PostOutboxResult204Response{}, nil
}

// GetConversationCursor tells a bot the platform time of the newest message Core holds for a
// conversation, so it can resume after the right message (BOT-10).
func (b *botAPI) GetConversationCursor(ctx context.Context, req botapi.GetConversationCursorRequestObject) (botapi.GetConversationCursorResponseObject, error) {
	bot, err := botFrom(ctx)
	if err != nil {
		return nil, err
	}
	ident, err := b.st.Q().IdentityByConversation(ctx, dbq.IdentityByConversationParams{BotInstanceID: bot.InstanceID, ConversationID: &req.Id})
	if err != nil {
		return botapi.GetConversationCursor200JSONResponse{}, nil // unknown conversation: nothing held
	}
	var last *time.Time
	err = b.st.InUserRead(ctx, ident.UserID, func(q *dbq.Queries) error {
		at, err := q.ConversationLastMessageAt(ctx, dbq.ConversationLastMessageAtParams{UserID: ident.UserID,
			SourceBotInstanceID: uuid.NullUUID{UUID: bot.InstanceID, Valid: true}, SourceConversationID: &req.Id})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err == nil {
			last = &at
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return botapi.GetConversationCursor200JSONResponse{LastMessageAt: last}, nil
}

// GetOutboxAttachment lets a bot fetch a file that a reminder it has claimed lists, and nothing else
// (BOT-15, SEC-BOT-3): the item must be claimed by this instance, the file must be in its payload,
// and the bytes come from the item's own user.
func (b *botAPI) GetOutboxAttachment(ctx context.Context, req botapi.GetOutboxAttachmentRequestObject) (botapi.GetOutboxAttachmentResponseObject, error) {
	bot, err := botFrom(ctx)
	if err != nil {
		return nil, err
	}
	user, err := b.outbox.AttachmentOwner(ctx, bot.InstanceID, req.Id, req.AttachmentId)
	if err != nil {
		return nil, err
	}
	reader, att, err := b.blobs.Open(ctx, user, req.AttachmentId)
	if err != nil {
		return nil, err
	}
	return botapi.GetOutboxAttachment200ApplicationoctetStreamResponse{Body: reader, ContentLength: att.Size}, nil
}
