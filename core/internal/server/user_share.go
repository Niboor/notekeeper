package server

import (
	"context"
	"net/http"
	"time"

	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"github.com/Niboor/notekeeper/core/internal/shares"
)

func shareLinkOf(l shares.Link) userapi.ShareLink {
	return userapi.ShareLink{Id: l.ID, NoteId: l.NoteID, Excerpt: l.Excerpt, CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt,
		LastAccessedAt: l.LastAccessedAt, ViewCount: int(l.ViewCount), NoteActive: l.NoteActive}
}

func (u *userAPI) CreateShareLink(ctx context.Context, req userapi.CreateShareLinkRequestObject) (userapi.CreateShareLinkResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	link, token, err := u.shares.Create(ctx, actorOf(p), p.UserID, req.Id, string(req.Body.ExpiresIn))
	if err != nil {
		return nil, err
	}
	// The token travels in the fragment, which browsers never send to a server (CORE-SH10, SEC-SHR-7).
	return userapi.CreateShareLink201JSONResponse{Link: shareLinkOf(link), Url: u.shareURL + "/s#" + token}, nil
}

func (u *userAPI) ListShareLinks(ctx context.Context, req userapi.ListShareLinksRequestObject) (userapi.ListShareLinksResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	links, err := u.shares.List(ctx, p.UserID, req.Params.NoteId)
	if err != nil {
		return nil, err
	}
	out := userapi.ListShareLinks200JSONResponse{Items: make([]userapi.ShareLink, len(links)), Enabled: u.shares.Cfg.Enabled,
		MaxLifetimeSeconds: int(u.shares.Cfg.MaxLifetime / time.Second)}
	for i, l := range links {
		out.Items[i] = shareLinkOf(l)
	}
	return out, nil
}

func (u *userAPI) RevokeShareLink(ctx context.Context, req userapi.RevokeShareLinkRequestObject) (userapi.RevokeShareLinkResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := u.shares.Revoke(ctx, actorOf(p), p.UserID, req.Id); err != nil {
		return nil, err
	}
	return userapi.RevokeShareLink204Response{}, nil
}

func (u *userAPI) RevokeAllShareLinks(ctx context.Context, _ userapi.RevokeAllShareLinksRequestObject) (userapi.RevokeAllShareLinksResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	n, err := u.shares.RevokeAll(ctx, actorOf(p), p.UserID)
	if err != nil {
		return nil, err
	}
	return userapi.RevokeAllShareLinks200JSONResponse{Revoked: n}, nil
}

func shareError(err error) *httpx.Error {
	switch {
	case isErr(err, shares.ErrDisabled):
		return httpx.NewError(http.StatusForbidden, "sharing_disabled")
	case isErr(err, shares.ErrInvalid):
		return httpx.NewError(http.StatusBadRequest, "invalid_expiry")
	case isErr(err, shares.ErrNotActive):
		return httpx.NewError(http.StatusConflict, "note_not_active")
	case isErr(err, shares.ErrLimit):
		return httpx.NewError(http.StatusTooManyRequests, "share_limit")
	}
	return nil
}
