package server

import (
	"context"
	"github.com/Niboor/notekeeper/core/internal/httpx"

	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/bots"
	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

func actorOf(p accounts.Principal) accounts.Actor {
	kind := "user"
	if p.IsAdmin {
		kind = "admin"
	}
	id := p.UserID
	return accounts.Actor{Kind: kind, ID: &id}
}

func adminUserOf(u accounts.UserSummary) userapi.AdminUser {
	return userapi.AdminUser{Id: u.ID, Username: u.Username, DisplayName: u.DisplayName, Status: userapi.AdminUserStatus(u.Status),
		IsAdmin: u.IsAdmin, CreatedAt: u.CreatedAt, UsedBytes: u.UsedBytes, QuotaBytes: u.QuotaBytes}
}

func (u *userAPI) AdminListUsers(ctx context.Context, _ userapi.AdminListUsersRequestObject) (userapi.AdminListUsersResponseObject, error) {
	rows, err := u.accts.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := userapi.AdminListUsers200JSONResponse{Items: make([]userapi.AdminUser, len(rows))}
	for i, r := range rows {
		out.Items[i] = adminUserOf(r)
	}
	return out, nil
}

func (u *userAPI) AdminCreateUser(ctx context.Context, req userapi.AdminCreateUserRequestObject) (userapi.AdminCreateUserResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	in := accounts.CreateUserInput{Username: req.Body.Username}
	if req.Body.DisplayName != nil {
		in.DisplayName = *req.Body.DisplayName
	}
	if req.Body.Email != nil {
		in.Email = *req.Body.Email
	}
	if req.Body.Timezone != nil {
		in.Timezone = *req.Body.Timezone
	}
	row, err := u.accts.CreateUser(ctx, actorOf(p), in)
	if err != nil {
		return nil, err
	}
	return userapi.AdminCreateUser201JSONResponse(adminUserOf(accounts.UserSummary{ID: row.ID, Username: row.Username,
		DisplayName: row.DisplayName, Status: row.Status, IsAdmin: row.IsAdmin, CreatedAt: row.CreatedAt})), nil
}

func (u *userAPI) AdminUpdateUser(ctx context.Context, req userapi.AdminUpdateUserRequestObject) (userapi.AdminUpdateUserResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	if req.Body.Disabled != nil {
		if err := u.accts.SetDisabled(ctx, actorOf(p), req.Id, *req.Body.Disabled); err != nil {
			return nil, err
		}
	}
	if req.Body.QuotaBytes != nil || (req.Body.ResetQuota != nil && *req.Body.ResetQuota) {
		quota := req.Body.QuotaBytes // nil resets to the deployment default
		if err := u.accts.SetQuota(ctx, actorOf(p), req.Id, quota); err != nil {
			return nil, err
		}
	}
	return userapi.AdminUpdateUser204Response{}, nil
}

func (u *userAPI) AdminIssueActivationLink(ctx context.Context, req userapi.AdminIssueActivationLinkRequestObject) (userapi.AdminIssueActivationLinkResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	link, err := u.accts.IssueActivation(ctx, actorOf(p), req.Id)
	if err != nil {
		return nil, err
	}
	return userapi.AdminIssueActivationLink201JSONResponse{Token: link.Token, Path: "/activate#" + link.Token, ExpiresAt: link.Expires}, nil
}

func botInstanceOf(i dbq.BotInstance, creds []dbq.ListBotCredentialsRow) userapi.AdminBotInstance {
	out := userapi.AdminBotInstance{Id: i.ID, Type: i.Type, Name: i.Name, IdentityDomain: i.IdentityDomain, Address: i.Address,
		Status: userapi.AdminBotInstanceStatus(i.Status), LastSeenAt: i.LastSeenAt, CreatedAt: i.CreatedAt}
	if creds != nil {
		list := make([]userapi.BotCredentialInfo, len(creds))
		for k, c := range creds {
			list[k] = userapi.BotCredentialInfo{Id: c.ID, ClientId: c.ClientID, Scopes: c.Scopes, CreatedAt: c.CreatedAt, DisabledAt: c.DisabledAt}
		}
		out.Credentials = &list
	}
	return out
}

func (u *userAPI) AdminListBotInstances(ctx context.Context, _ userapi.AdminListBotInstancesRequestObject) (userapi.AdminListBotInstancesResponseObject, error) {
	rows, err := u.bots.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := userapi.AdminListBotInstances200JSONResponse{Items: make([]userapi.AdminBotInstance, len(rows))}
	for i, r := range rows {
		creds, err := u.bots.ListCredentials(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		out.Items[i] = botInstanceOf(r, creds)
	}
	return out, nil
}

func (u *userAPI) AdminCreateBotInstance(ctx context.Context, req userapi.AdminCreateBotInstanceRequestObject) (userapi.AdminCreateBotInstanceResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	in := bots.CreateInstanceInput{Type: req.Body.Type, Name: req.Body.Name}
	if req.Body.IdentityDomain != nil {
		in.IdentityDomain = *req.Body.IdentityDomain
	}
	row, err := u.bots.CreateInstance(ctx, actorOf(p), in)
	if err != nil {
		return nil, err
	}
	return userapi.AdminCreateBotInstance201JSONResponse(botInstanceOf(row, nil)), nil
}

func (u *userAPI) AdminUpdateBotInstance(ctx context.Context, req userapi.AdminUpdateBotInstanceRequestObject) (userapi.AdminUpdateBotInstanceResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	if err := u.bots.SetInstanceStatus(ctx, actorOf(p), req.Id, string(req.Body.Status)); err != nil {
		return nil, err
	}
	return userapi.AdminUpdateBotInstance204Response{}, nil
}

func (u *userAPI) AdminCreateBotCredential(ctx context.Context, req userapi.AdminCreateBotCredentialRequestObject) (userapi.AdminCreateBotCredentialResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	var scopes []string
	if req.Body != nil && req.Body.Scopes != nil {
		for _, s := range *req.Body.Scopes {
			scopes = append(scopes, string(s))
		}
	}
	c, err := u.bots.CreateCredential(ctx, actorOf(p), req.Id, scopes)
	if err != nil {
		return nil, err
	}
	return userapi.AdminCreateBotCredential201JSONResponse{Id: c.ID, ClientId: c.ClientID, Bearer: c.Bearer}, nil
}

func (u *userAPI) AdminDisableBotCredential(ctx context.Context, req userapi.AdminDisableBotCredentialRequestObject) (userapi.AdminDisableBotCredentialResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := u.bots.DisableCredential(ctx, actorOf(p), req.Id, req.CredentialId); err != nil {
		return nil, err
	}
	return userapi.AdminDisableBotCredential204Response{}, nil
}

func (u *userAPI) AdminDeleteUser(ctx context.Context, req userapi.AdminDeleteUserRequestObject) (userapi.AdminDeleteUserResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := u.accts.RequestDeletion(ctx, actorOf(p), req.Id); err != nil {
		return nil, err
	}
	return userapi.AdminDeleteUser202Response{}, nil
}

// DeleteMyAccount deletes the caller's own account after the password is confirmed (AUTH-U4).
func (u *userAPI) DeleteMyAccount(ctx context.Context, req userapi.DeleteMyAccountRequestObject) (userapi.DeleteMyAccountResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	if err := u.accts.ConfirmPassword(ctx, p, req.Body.Password); err != nil {
		return nil, err
	}
	if err := u.accts.RequestDeletion(ctx, actorOf(p), p.UserID); err != nil {
		return nil, err
	}
	w, _ := httpx.HTTPFrom(ctx)
	clearAuthCookies(w)
	return userapi.DeleteMyAccount202Response{}, nil
}
