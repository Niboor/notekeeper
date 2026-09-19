package accounts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/auth"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

const tokenActivation = "activation"

// Actor identifies who performed an administrative action, for the audit log.
type Actor struct {
	Kind string // "admin" or "system" (operator CLI)
	ID   *uuid.UUID
}

// CreateUserInput describes a new account. There is no password: the user sets it through an
// activation link (AUTH-U8), so the admin never sees or chooses one.
type CreateUserInput struct {
	Username    string
	DisplayName string
	Email       string
	Timezone    string
}

func (s *Service) createUser(ctx context.Context, q *dbq.Queries, in CreateUserInput, admin bool) (dbq.User, error) {
	username := NormaliseUsername(in.Username)
	if !usernameRE.MatchString(username) {
		return dbq.User{}, fmt.Errorf("%w: username must be 1-32 characters: letters, digits, dot, dash, underscore", ErrInvalidInput)
	}
	display := in.DisplayName
	if display == "" {
		display = username
	}
	if len([]rune(display)) > 100 {
		return dbq.User{}, fmt.Errorf("%w: display name too long", ErrInvalidInput)
	}
	tz := in.Timezone
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return dbq.User{}, fmt.Errorf("%w: unknown timezone", ErrInvalidInput)
	}
	var email *string
	if in.Email != "" {
		email = &in.Email
	}
	id, err := uuid.NewV7()
	if err != nil {
		return dbq.User{}, err
	}
	u, err := q.CreateUser(ctx, dbq.CreateUserParams{ID: id, Username: username, DisplayName: display, Email: email, Timezone: tz, IsAdmin: admin})
	if store.IsUniqueViolation(err, "") {
		return dbq.User{}, ErrConflict
	}
	if err != nil {
		return dbq.User{}, err
	}
	return u, q.EnsureUserStorage(ctx, u.ID)
}

// CreateUser creates a pending account (AUTH-U6).
func (s *Service) CreateUser(ctx context.Context, actor Actor, in CreateUserInput) (dbq.User, error) {
	var u dbq.User
	err := s.St.InTx(ctx, func(q *dbq.Queries) error {
		var err error
		if u, err = s.createUser(ctx, q, in, false); err != nil {
			return err
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "user.created",
			TargetKind: "user", TargetID: &u.ID})
	})
	return u, err
}

// ActivationLink is a freshly issued single-use token. The token is shown once; only its hash is stored.
type ActivationLink struct {
	UserID  uuid.UUID
	Token   string
	Expires time.Time
}

// IssueActivation creates an activation link for a user. It replaces older links, clears the
// current password, leaves the account pending and revokes every session (design decision D3,
// SEC-ADM-2): whoever held the old credentials is signed out at once.
func (s *Service) IssueActivation(ctx context.Context, actor Actor, userID uuid.UUID) (ActivationLink, error) {
	var link ActivationLink
	err := s.St.InTx(ctx, func(q *dbq.Queries) error {
		var err error
		link, err = s.issueActivation(ctx, q, actor, userID)
		return err
	})
	return link, err
}

func (s *Service) issueActivation(ctx context.Context, q *dbq.Queries, actor Actor, userID uuid.UUID) (ActivationLink, error) {
	u, err := q.GetUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationLink{}, store.ErrNotFound
	}
	if err != nil {
		return ActivationLink{}, err
	}
	if u.Status == "disabled" || u.Status == "deleting" {
		return ActivationLink{}, ErrConflict
	}
	now := s.Now()
	if err := q.DeleteUserTokens(ctx, dbq.DeleteUserTokensParams{UserID: userID, Kind: tokenActivation}); err != nil {
		return ActivationLink{}, err
	}
	if err := q.ClearUserPassword(ctx, dbq.ClearUserPasswordParams{ID: userID, UpdatedAt: now}); err != nil {
		return ActivationLink{}, err
	}
	if err := s.revokeAll(ctx, q, userID, nil, "activation_link_issued"); err != nil {
		return ActivationLink{}, err
	}
	token := auth.RandomToken(24) // 192 bits
	id, _ := uuid.NewV7()
	expires := now.Add(s.Cfg.ActivationTTL)
	var by uuid.NullUUID
	if actor.ID != nil {
		by = uuid.NullUUID{UUID: *actor.ID, Valid: true}
	}
	if err := q.InsertUserToken(ctx, dbq.InsertUserTokenParams{ID: id, UserID: userID, Kind: tokenActivation,
		TokenHash: auth.HashToken(token), ExpiresAt: expires, CreatedBy: by}); err != nil {
		return ActivationLink{}, err
	}
	if err := store.Audit(ctx, q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "user.activation_link_issued",
		TargetKind: "user", TargetID: &userID}); err != nil {
		return ActivationLink{}, err
	}
	return ActivationLink{UserID: userID, Token: token, Expires: expires}, nil
}

// Bootstrap creates the one admin account and its first activation link. It is run only by the
// operator CLI (design decision D4) and refuses when an admin exists; the partial unique index
// users_single_admin enforces that in the database even under a race (AUTH-U7, SEC-AUTH-13).
func (s *Service) Bootstrap(ctx context.Context, username string) (ActivationLink, error) {
	var link ActivationLink
	err := s.St.InTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.GetAdmin(ctx); err == nil {
			return fmt.Errorf("%w: an admin already exists", ErrConflict)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		u, err := s.createUser(ctx, q, CreateUserInput{Username: username}, true)
		if err != nil {
			return err
		}
		link, err = s.issueActivation(ctx, q, Actor{Kind: "system"}, u.ID)
		return err
	})
	return link, err
}

// ActivationLinkFor issues a new link for an existing user by username (operator recovery tool).
func (s *Service) ActivationLinkFor(ctx context.Context, username string) (ActivationLink, error) {
	u, err := s.St.Q().GetUserByUsername(ctx, NormaliseUsername(username))
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationLink{}, store.ErrNotFound
	}
	if err != nil {
		return ActivationLink{}, err
	}
	return s.IssueActivation(ctx, Actor{Kind: "system"}, u.ID)
}

// Activate consumes an activation token, sets the password, and signs the user in (AUTH-U8).
// The token is single use and is consumed atomically (SEC-AUTH-4). A password that fails the
// policy is rejected without consuming the token.
func (s *Service) Activate(ctx context.Context, token, password string, c ClientInfo) (Tokens, error) {
	if err := auth.CheckPolicy(password); err != nil {
		return Tokens{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	hash, err := s.Hash.Hash(ctx, password)
	if err != nil {
		return Tokens{}, err
	}
	var out Tokens
	var sid uuid.UUID
	var user dbq.User
	err = s.St.InTx(ctx, func(q *dbq.Queries) error {
		userID, err := q.ConsumeUserToken(ctx, dbq.ConsumeUserTokenParams{
			TokenHash: auth.HashToken(token), Kind: tokenActivation, UsedAt: ptr(s.Now()),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		n, err := q.ActivateUser(ctx, dbq.ActivateUserParams{ID: userID, PasswordHash: &hash, UpdatedAt: s.Now()})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrInvalidToken // disabled or already active accounts cannot be activated
		}
		if user, err = q.GetUser(ctx, userID); err != nil {
			return err
		}
		if out, sid, err = s.newSession(ctx, q, userID, true, c); err != nil {
			return err
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: "user", ActorID: &userID, Action: "user.activated",
			TargetKind: "user", TargetID: &userID})
	})
	if err != nil {
		return Tokens{}, err
	}
	out.Principal = s.principalOf(user, sid)
	return out, nil
}

// SetDisabled disables or re-enables an account (AUTH-U6). Disabling revokes every session, and
// ingest, deliveries and share links stop because they check the status live (SEC-BOT-13).
func (s *Service) SetDisabled(ctx context.Context, actor Actor, userID uuid.UUID, disabled bool) error {
	return s.St.InTx(ctx, func(q *dbq.Queries) error {
		u, err := q.GetUser(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if u.IsAdmin && disabled {
			return fmt.Errorf("%w: the admin cannot be disabled", ErrConflict)
		}
		if u.Status == "deleting" {
			return ErrConflict
		}
		status := "active"
		switch {
		case disabled:
			status = "disabled"
		case u.PasswordHash == nil:
			status = "pending"
		}
		if err := q.SetUserStatus(ctx, dbq.SetUserStatusParams{ID: userID, Status: status, UpdatedAt: s.Now()}); err != nil {
			return err
		}
		action := "user.enabled"
		if disabled {
			action = "user.disabled"
			if err := s.revokeAll(ctx, q, userID, nil, "user_disabled"); err != nil {
				return err
			}
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: action,
			TargetKind: "user", TargetID: &userID})
	})
}

// UserSummary is the admin's view of an account: metadata only, never content (SEC-ADM-1).
type UserSummary struct {
	ID          uuid.UUID
	Username    string
	DisplayName string
	Status      string
	IsAdmin     bool
	CreatedAt   time.Time
	UsedBytes   int64
	QuotaBytes  *int64
}

// ListUsers lists all accounts with their storage use.
func (s *Service) ListUsers(ctx context.Context) ([]UserSummary, error) {
	rows, err := s.St.Q().ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]UserSummary, len(rows))
	for i, r := range rows {
		out[i] = UserSummary{ID: r.ID, Username: r.Username, DisplayName: r.DisplayName, Status: r.Status,
			IsAdmin: r.IsAdmin, CreatedAt: r.CreatedAt, UsedBytes: r.UsedBytes, QuotaBytes: r.QuotaBytes}
	}
	return out, nil
}

// SetQuota sets a user's storage quota in bytes (nil = deployment default).
func (s *Service) SetQuota(ctx context.Context, actor Actor, userID uuid.UUID, quota *int64) error {
	return s.St.InTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.GetUser(ctx, userID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return store.ErrNotFound
			}
			return err
		}
		if err := q.EnsureUserStorage(ctx, userID); err != nil {
			return err
		}
		if err := q.SetQuota(ctx, dbq.SetQuotaParams{UserID: userID, QuotaBytes: quota}); err != nil {
			return err
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "user.quota_changed",
			TargetKind: "user", TargetID: &userID})
	})
}

// Me returns the current user's profile.
func (s *Service) Me(ctx context.Context, p Principal) (dbq.User, error) {
	return s.St.Q().GetUser(ctx, p.UserID)
}
