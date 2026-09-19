package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

func (u *userAPI) clientInfo(ctx context.Context, kind string) accounts.ClientInfo {
	_, r := httpx.HTTPFrom(ctx)
	if r == nil {
		return accounts.ClientInfo{Kind: kind}
	}
	return accounts.ClientInfo{IP: httpx.ClientIP(r, u.trusted), UserAgent: r.UserAgent(), Kind: kind}
}

func setAuthCookies(w http.ResponseWriter, t accounts.Tokens, now time.Time) {
	maxAge := 0 // session cookie: gone when the browser closes (AUTH-C11)
	if t.Persistent {
		maxAge = int(t.SessionExpiry.Sub(now).Seconds())
	}
	http.SetCookie(w, &http.Cookie{Name: accessCookie, Value: t.Access, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: refreshCookie, Value: t.Refresh, Path: refreshPath, MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func clearAuthCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: accessCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: refreshCookie, Value: "", Path: refreshPath, MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func meOf(row dbq.User) userapi.Me {
	settings := map[string]interface{}{}
	_ = json.Unmarshal(row.Settings, &settings)
	return userapi.Me{Id: row.ID, Username: row.Username, DisplayName: row.DisplayName, IsAdmin: row.IsAdmin,
		Timezone: row.Timezone, Settings: settings}
}

// authResult builds the response of login, activation and refresh. Web clients receive cookies
// only; native clients get the tokens in the body instead (docs/design/03-auth.md section 2.2).
func (u *userAPI) authResult(ctx context.Context, t accounts.Tokens, native bool) (userapi.AuthResult, error) {
	row, err := u.accts.Me(ctx, t.Principal)
	if err != nil {
		return userapi.AuthResult{}, err
	}
	res := userapi.AuthResult{User: meOf(row), ExpiresAt: t.AccessExpiry}
	if native {
		res.AccessToken, res.RefreshToken = &t.Access, &t.Refresh
	} else if w, _ := httpx.HTTPFrom(ctx); w != nil {
		setAuthCookies(w, t, u.accts.Now())
	}
	return res, nil
}

func (u *userAPI) Login(ctx context.Context, req userapi.LoginRequestObject) (userapi.LoginResponseObject, error) {
	if req.Body == nil {
		return nil, errBadRequest
	}
	native := req.Body.ClientKind != nil && *req.Body.ClientKind == userapi.Native
	kind := "web"
	if native {
		kind = "native"
	}
	remember := req.Body.Remember == nil || *req.Body.Remember
	t, err := u.accts.Login(ctx, accounts.LoginInput{Username: req.Body.Username, Password: req.Body.Password,
		Remember: remember, Client: u.clientInfo(ctx, kind)})
	if err != nil {
		return nil, err
	}
	res, err := u.authResult(ctx, t, native)
	if err != nil {
		return nil, err
	}
	return userapi.Login200JSONResponse(res), nil
}

func (u *userAPI) RefreshSession(ctx context.Context, req userapi.RefreshSessionRequestObject) (userapi.RefreshSessionResponseObject, error) {
	w, r := httpx.HTTPFrom(ctx)
	token, native := "", false
	if req.Body != nil && req.Body.RefreshToken != nil {
		token, native = *req.Body.RefreshToken, true
	} else if c, err := r.Cookie(refreshCookie); err == nil {
		token = c.Value
	}
	t, err := u.accts.Refresh(ctx, token)
	if err != nil {
		if errors.Is(err, accounts.ErrUnauthenticated) {
			if !native {
				clearAuthCookies(w)
			}
			// A different code from "unauthenticated" (missing credentials): the client goes to the login page.
			return nil, httpx.NewError(http.StatusUnauthorized, "session_expired")
		}
		return nil, err
	}
	res, err := u.authResult(ctx, t, native)
	if err != nil {
		return nil, err
	}
	return userapi.RefreshSession200JSONResponse(res), nil
}

func (u *userAPI) Logout(ctx context.Context, _ userapi.LogoutRequestObject) (userapi.LogoutResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := u.accts.Logout(ctx, p); err != nil {
		return nil, err
	}
	if w, _ := httpx.HTTPFrom(ctx); w != nil {
		clearAuthCookies(w)
	}
	return userapi.Logout204Response{}, nil
}

func (u *userAPI) Activate(ctx context.Context, req userapi.ActivateRequestObject) (userapi.ActivateResponseObject, error) {
	if req.Body == nil {
		return nil, errBadRequest
	}
	t, err := u.accts.Activate(ctx, req.Body.Token, req.Body.Password, u.clientInfo(ctx, "web"))
	if err != nil {
		return nil, err
	}
	res, err := u.authResult(ctx, t, false)
	if err != nil {
		return nil, err
	}
	return userapi.Activate200JSONResponse(res), nil
}

func (u *userAPI) GetMe(ctx context.Context, _ userapi.GetMeRequestObject) (userapi.GetMeResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	row, err := u.accts.Me(ctx, p)
	if err != nil {
		return nil, err
	}
	return userapi.GetMe200JSONResponse(meOf(row)), nil
}

func (u *userAPI) UpdateMe(ctx context.Context, req userapi.UpdateMeRequestObject) (userapi.UpdateMeResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	var settings []byte
	if req.Body.Settings != nil {
		if settings, err = json.Marshal(*req.Body.Settings); err != nil || len(settings) > 8192 {
			return nil, errBadRequest.WithDetail("settings too large")
		}
	}
	row, err := u.accts.UpdateProfile(ctx, p, req.Body.DisplayName, req.Body.Timezone, settings)
	if err != nil {
		return nil, err
	}
	return userapi.UpdateMe200JSONResponse(meOf(row)), nil
}

func (u *userAPI) ChangePassword(ctx context.Context, req userapi.ChangePasswordRequestObject) (userapi.ChangePasswordResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	if err := u.accts.ChangePassword(ctx, p, req.Body.CurrentPassword, req.Body.NewPassword, u.clientInfo(ctx, "web")); err != nil {
		return nil, err
	}
	return userapi.ChangePassword204Response{}, nil
}

func (u *userAPI) ListSessions(ctx context.Context, _ userapi.ListSessionsRequestObject) (userapi.ListSessionsResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	list, err := u.accts.ListSessions(ctx, p)
	if err != nil {
		return nil, err
	}
	out := userapi.ListSessions200JSONResponse{Items: make([]userapi.Session, len(list))}
	for i, s := range list {
		out.Items[i] = userapi.Session{Id: s.ID, Label: s.Label, ClientKind: s.ClientKind, CreatedAt: s.CreatedAt,
			LastUsedAt: s.LastUsedAt, ExpiresAt: s.ExpiresAt, Current: s.Current}
	}
	return out, nil
}

func (u *userAPI) RevokeSession(ctx context.Context, req userapi.RevokeSessionRequestObject) (userapi.RevokeSessionResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := u.accts.RevokeSession(ctx, p, req.Id); err != nil {
		return nil, err
	}
	return userapi.RevokeSession204Response{}, nil
}

func (u *userAPI) RevokeAllSessions(ctx context.Context, req userapi.RevokeAllSessionsRequestObject) (userapi.RevokeAllSessionsResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	keep := req.Body != nil && req.Body.KeepCurrent != nil && *req.Body.KeepCurrent
	if err := u.accts.RevokeAllSessions(ctx, p, keep); err != nil {
		return nil, err
	}
	if !keep {
		if w, _ := httpx.HTTPFrom(ctx); w != nil {
			clearAuthCookies(w)
		}
	}
	return userapi.RevokeAllSessions204Response{}, nil
}
