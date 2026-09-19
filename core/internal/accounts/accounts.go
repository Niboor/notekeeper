// Package accounts implements users, sessions and the flows around them: login, silent renewal
// with a grace window, activation, password change, revocation (docs/design/03-auth.md).
package accounts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/text/unicode/norm"

	"github.com/Niboor/notekeeper/core/internal/auth"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
	"github.com/Niboor/notekeeper/core/internal/throttle"
)

// Errors returned by the service. Handlers map them to responses; none reveals whether an
// account exists (SEC-AUTH-3).
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthenticated    = errors.New("unauthenticated")
	ErrInvalidToken       = errors.New("invalid or expired link")
	ErrConflict           = errors.New("conflict")
	ErrInvalidInput       = errors.New("invalid input")
)

// ThrottledError is returned when too many failures were seen; RetryAfter says how long to wait.
type ThrottledError struct{ RetryAfter time.Duration }

func (e *ThrottledError) Error() string { return "too many attempts" }

// Config holds the lifetimes and cost settings (docs/design/08 section 3).
type Config struct {
	AccessTTL        time.Duration
	IdleLifetime     time.Duration // sliding session lifetime (AUTH-C9)
	AbsoluteLifetime time.Duration // 0 = unlimited
	SessionOnly      time.Duration // lifetime of a "do not remember me" session (AUTH-C11)
	RefreshGrace     time.Duration // SEC-AUTH-7
	ActivationTTL    time.Duration
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		AccessTTL: 15 * time.Minute, IdleLifetime: 90 * 24 * time.Hour, AbsoluteLifetime: 365 * 24 * time.Hour,
		SessionOnly: 12 * time.Hour, RefreshGrace: 60 * time.Second, ActivationTTL: 7 * 24 * time.Hour,
	}
}

// Service is the accounts service.
type Service struct {
	St   *store.Store
	Keys *auth.Keyring
	Hash *auth.Hasher
	Cfg  Config
	Log  *slog.Logger
	Now  func() time.Time

	throttle *throttle.Throttle
}

// New creates the service.
func New(st *store.Store, keys *auth.Keyring, hash *auth.Hasher, cfg Config, log *slog.Logger) *Service {
	s := &Service{St: st, Keys: keys, Hash: hash, Cfg: cfg, Log: log, Now: time.Now}
	s.throttle = &throttle.Throttle{St: st, Now: func() time.Time { return s.Now() }}
	return s
}

// Principal is the authenticated user behind a request. It is loaded from the database on
// every request, never trusted from a token (SEC-ISO-7).
type Principal struct {
	UserID      uuid.UUID
	SessionID   uuid.UUID
	Username    string
	DisplayName string
	Timezone    string
	IsAdmin     bool
	ClientKind  string
}

// Tokens is the outcome of a login, activation or refresh.
type Tokens struct {
	Principal    Principal
	Access       string
	AccessExpiry time.Time
	Refresh      string
	// SessionExpiry is when the session (and so the cookies) lapse without further use.
	SessionExpiry time.Time
	// Persistent is false for "do not remember me": the cookies then live for the browser session.
	Persistent bool
}

// ClientInfo describes the caller for the session list and throttling.
type ClientInfo struct {
	IP        string
	UserAgent string
	Kind      string // "web" (default) or "native"
}

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// NormaliseUsername lower-cases and NFC-normalises a username (docs/design/03-auth.md section 3).
func NormaliseUsername(s string) string {
	return strings.ToLower(norm.NFC.String(strings.TrimSpace(s)))
}

func (s *Service) newSession(ctx context.Context, q *dbq.Queries, userID uuid.UUID, remember bool, c ClientInfo) (Tokens, uuid.UUID, error) {
	now := s.Now()
	idle, absolute := s.Cfg.IdleLifetime, s.Cfg.AbsoluteLifetime
	if absolute == 0 {
		absolute = 100 * 365 * 24 * time.Hour
	}
	if !remember {
		idle, absolute = s.Cfg.SessionOnly, s.Cfg.SessionOnly
	}
	absExp := now.Add(absolute)
	exp := minTime(now.Add(idle), absExp)
	kind := c.Kind
	if kind == "" {
		kind = "web"
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Tokens{}, uuid.Nil, err
	}
	if err := q.InsertSession(ctx, dbq.InsertSessionParams{
		ID: id, UserID: userID, ClientKind: kind, Label: SessionLabel(c.UserAgent),
		CreatedAt: now, ExpiresAt: exp, AbsoluteExpiresAt: absExp,
	}); err != nil {
		return Tokens{}, uuid.Nil, err
	}
	return s.tokens(id, 0, exp, remember), id, nil
}

func (s *Service) tokens(session uuid.UUID, generation int32, sessionExpiry time.Time, persistent bool) Tokens {
	accessExp := s.Now().Add(s.Cfg.AccessTTL)
	return Tokens{
		Access: s.Keys.Access(session, accessExp), AccessExpiry: accessExp,
		Refresh: s.Keys.Refresh(session, generation), SessionExpiry: sessionExpiry, Persistent: persistent,
	}
}

// LoginInput is the data of a login attempt.
type LoginInput struct {
	Username, Password string
	Remember           bool
	Client             ClientInfo
}

// Login verifies credentials, throttles failures and creates a fresh session (SEC-AUTH-9).
func (s *Service) Login(ctx context.Context, in LoginInput) (Tokens, error) {
	username := NormaliseUsername(in.Username)
	acctKey, ipKey := "acct:"+username, "ip:"+in.Client.IP
	wait, err := s.throttle.Blocked(ctx, acctKey, ipKey)
	if err != nil {
		return Tokens{}, err
	}
	if wait > 0 {
		return Tokens{}, &ThrottledError{RetryAfter: wait}
	}

	u, err := s.St.Q().GetUserByUsername(ctx, username)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Tokens{}, err
	}
	ok := false
	if found && u.Status == "active" && u.PasswordHash != nil {
		var rehash bool
		ok, rehash, err = s.Hash.Verify(ctx, in.Password, *u.PasswordHash)
		if err != nil {
			return Tokens{}, err
		}
		if ok && rehash {
			if h, herr := s.Hash.Hash(ctx, in.Password); herr == nil {
				_ = s.St.Q().SetPasswordHash(ctx, dbq.SetPasswordHashParams{ID: u.ID, PasswordHash: &h, UpdatedAt: s.Now()})
			}
		}
	} else {
		s.Hash.VerifyDummy(ctx, in.Password) // equal work whether or not the account exists
	}
	if !ok {
		_ = s.throttle.Fail(ctx, acctKey, throttle.Account)
		_ = s.throttle.Fail(ctx, ipKey, throttle.IP)
		_ = store.Audit(ctx, s.St.Q(), store.AuditEntry{ActorKind: "anonymous", Action: "login.failed",
			Detail: map[string]any{"known_user": found}})
		return Tokens{}, ErrInvalidCredentials
	}
	_ = s.throttle.Reset(ctx, acctKey)

	var out Tokens
	var sid uuid.UUID
	err = s.St.InTx(ctx, func(q *dbq.Queries) error {
		var err error
		if out, sid, err = s.newSession(ctx, q, u.ID, in.Remember, in.Client); err != nil {
			return err
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: "user", ActorID: &u.ID, Action: "login.succeeded",
			TargetKind: "session", TargetID: &sid})
	})
	if err != nil {
		return Tokens{}, err
	}
	out.Principal = s.principalOf(u, sid)
	return out, nil
}

func (s *Service) principalOf(u dbq.User, session uuid.UUID) Principal {
	return Principal{UserID: u.ID, SessionID: session, Username: u.Username, DisplayName: u.DisplayName,
		Timezone: u.Timezone, IsAdmin: u.IsAdmin}
}

// Authenticate resolves an access token to its principal. Session state, user status and
// admin rights are read from the database each time, so revocation is immediate (SEC-AUTH-6).
func (s *Service) Authenticate(ctx context.Context, access string) (*Principal, error) {
	sid, err := s.Keys.ParseAccess(access, s.Now())
	if err != nil {
		return nil, ErrUnauthenticated
	}
	row, err := s.St.Q().GetSessionPrincipal(ctx, sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	now := s.Now()
	if row.RevokedAt != nil || !row.ExpiresAt.After(now) || !row.AbsoluteExpiresAt.After(now) || row.Status != "active" {
		return nil, ErrUnauthenticated
	}
	return &Principal{UserID: row.UserID, SessionID: row.ID, Username: row.Username, DisplayName: row.DisplayName,
		Timezone: row.Timezone, IsAdmin: row.IsAdmin, ClientKind: row.ClientKind}, nil
}

// Refresh renews the tokens of a session (docs/design/03-auth.md section 2.3). A second request
// with the previous refresh token inside the grace window gets the same successor, so a race
// between tabs signs nobody out; reuse of an older token revokes the session (SEC-AUTH-7).
func (s *Service) Refresh(ctx context.Context, refresh string) (Tokens, error) {
	sid, gen, err := s.Keys.ParseRefresh(refresh)
	if err != nil {
		return Tokens{}, ErrUnauthenticated
	}
	var out Tokens
	var reuse bool
	err = s.St.InTx(ctx, func(q *dbq.Queries) error {
		sess, err := q.GetSessionForUpdate(ctx, sid)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnauthenticated
		}
		if err != nil {
			return err
		}
		now := s.Now()
		if sess.RevokedAt != nil || !sess.ExpiresAt.After(now) || !sess.AbsoluteExpiresAt.After(now) {
			return ErrUnauthenticated
		}
		u, err := q.GetUser(ctx, sess.UserID)
		if err != nil || u.Status != "active" {
			return ErrUnauthenticated
		}
		persistent := sess.AbsoluteExpiresAt.Sub(sess.CreatedAt) > s.Cfg.SessionOnly
		idle := s.Cfg.IdleLifetime
		if !persistent {
			idle = s.Cfg.SessionOnly
		}
		switch {
		case gen == sess.RefreshGeneration:
			exp := minTime(now.Add(idle), sess.AbsoluteExpiresAt)
			grace := now.Add(s.Cfg.RefreshGrace)
			n, err := q.RotateSession(ctx, dbq.RotateSessionParams{
				ID: sid, RefreshGeneration: gen, PreviousValidUntil: &grace, LastRefreshedAt: now, ExpiresAt: exp,
			})
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrUnauthenticated
			}
			out = s.tokens(sid, gen+1, exp, persistent)
		case gen == sess.RefreshGeneration-1 && sess.PreviousValidUntil != nil && !now.After(*sess.PreviousValidUntil):
			out = s.tokens(sid, sess.RefreshGeneration, sess.ExpiresAt, persistent)
		case gen < sess.RefreshGeneration:
			// A rotated token was presented again after the grace window: it may have been stolen.
			if _, err := q.RevokeSession(ctx, dbq.RevokeSessionParams{ID: sid, RevokedAt: &now, RevokedReason: ptr("refresh_reuse")}); err != nil {
				return err
			}
			if err := store.Audit(ctx, q, store.AuditEntry{ActorKind: "system", ActorID: &sess.UserID, Action: "session.refresh_reuse",
				TargetKind: "session", TargetID: &sid}); err != nil {
				return err
			}
			if err := q.NotifySessions(ctx, "s:"+sid.String()); err != nil {
				return err
			}
			reuse = true
			return nil
		default:
			return ErrUnauthenticated
		}
		out.Principal = s.principalOf(u, sid)
		return nil
	})
	if err != nil {
		return Tokens{}, err
	}
	if reuse {
		return Tokens{}, ErrUnauthenticated
	}
	return out, nil
}

// Logout ends one session.
func (s *Service) Logout(ctx context.Context, p Principal) error {
	return s.revoke(ctx, p.UserID, []uuid.UUID{p.SessionID}, "logout")
}

func (s *Service) revoke(ctx context.Context, user uuid.UUID, sessions []uuid.UUID, reason string) error {
	now := s.Now()
	return s.St.InTx(ctx, func(q *dbq.Queries) error {
		for _, id := range sessions {
			if _, err := q.RevokeSession(ctx, dbq.RevokeSessionParams{ID: id, RevokedAt: &now, RevokedReason: &reason}); err != nil {
				return err
			}
			if err := store.Audit(ctx, q, store.AuditEntry{ActorKind: "user", ActorID: &user, Action: "session.revoked",
				TargetKind: "session", TargetID: &id, Detail: map[string]any{"reason": reason}}); err != nil {
				return err
			}
		}
		return nil
	})
}

// revokeAll revokes every session of a user (optionally sparing one) and announces it so open
// event streams close at once (SEC-ISO-5). It runs inside the caller's transaction.
func (s *Service) revokeAll(ctx context.Context, q *dbq.Queries, user uuid.UUID, except *uuid.UUID, reason string) error {
	now := s.Now()
	var ex uuid.NullUUID
	if except != nil {
		ex = uuid.NullUUID{UUID: *except, Valid: true}
	}
	ids, err := q.RevokeUserSessions(ctx, dbq.RevokeUserSessionsParams{UserID: user, RevokedAt: &now, RevokedReason: &reason, Except: ex})
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		// pg_notify inside the transaction: delivered only if it commits.
		return q.NotifySessions(ctx, "u:"+user.String())
	}
	return nil
}

// RevokeAllSessions signs the user out everywhere, optionally keeping the current session (AUTH-U10).
func (s *Service) RevokeAllSessions(ctx context.Context, p Principal, keepCurrent bool) error {
	return s.St.InTx(ctx, func(q *dbq.Queries) error {
		var except *uuid.UUID
		if keepCurrent {
			except = &p.SessionID
		}
		if err := s.revokeAll(ctx, q, p.UserID, except, "sign_out_everywhere"); err != nil {
			return err
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: "user", ActorID: &p.UserID, Action: "session.revoke_all"})
	})
}

// SessionInfo is one row of the session list.
type SessionInfo struct {
	ID          uuid.UUID
	Label       string
	ClientKind  string
	CreatedAt   time.Time
	LastUsedAt  time.Time
	ExpiresAt   time.Time
	Current     bool
}

// ListSessions lists the user's active sessions.
func (s *Service) ListSessions(ctx context.Context, p Principal) ([]SessionInfo, error) {
	rows, err := s.St.Q().ListUserSessions(ctx, dbq.ListUserSessionsParams{UserID: p.UserID, ExpiresAt: s.Now()})
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, len(rows))
	for i, r := range rows {
		out[i] = SessionInfo{ID: r.ID, Label: r.Label, ClientKind: r.ClientKind, CreatedAt: r.CreatedAt,
			LastUsedAt: r.LastRefreshedAt, ExpiresAt: r.ExpiresAt, Current: r.ID == p.SessionID}
	}
	return out, nil
}

// RevokeSession revokes one of the user's own sessions; another user's id is "not found".
func (s *Service) RevokeSession(ctx context.Context, p Principal, id uuid.UUID) error {
	if _, err := s.St.Q().GetUserSession(ctx, dbq.GetUserSessionParams{ID: id, UserID: p.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		return err
	}
	if err := s.revoke(ctx, p.UserID, []uuid.UUID{id}, "revoked_by_user"); err != nil {
		return err
	}
	return s.St.Notify(ctx, store.ChannelSessions, "s:"+id.String())
}

// ChangePassword sets a new password after verifying the current one, keeps the current
// session and revokes all others (AUTH-U4, SEC-AUTH-6).
func (s *Service) ChangePassword(ctx context.Context, p Principal, current, next string, c ClientInfo) error {
	acctKey := "acct:" + NormaliseUsername(p.Username)
	if wait, err := s.throttle.Blocked(ctx, acctKey); err != nil {
		return err
	} else if wait > 0 {
		return &ThrottledError{RetryAfter: wait}
	}
	u, err := s.St.Q().GetUser(ctx, p.UserID)
	if err != nil || u.PasswordHash == nil {
		return ErrUnauthenticated
	}
	ok, _, err := s.Hash.Verify(ctx, current, *u.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		_ = s.throttle.Fail(ctx, acctKey, throttle.Account)
		return ErrInvalidCredentials
	}
	if err := auth.CheckPolicy(next); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	h, err := s.Hash.Hash(ctx, next)
	if err != nil {
		return err
	}
	return s.St.InTx(ctx, func(q *dbq.Queries) error {
		if err := q.SetPasswordHash(ctx, dbq.SetPasswordHashParams{ID: u.ID, PasswordHash: &h, UpdatedAt: s.Now()}); err != nil {
			return err
		}
		if err := s.revokeAll(ctx, q, u.ID, &p.SessionID, "password_changed"); err != nil {
			return err
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: "user", ActorID: &u.ID, Action: "password.changed"})
	})
}

// SessionLabel makes a short description such as "Firefox on Linux" from a User-Agent header.
// It only reads the header; nothing else about the client is kept.
func SessionLabel(ua string) string {
	browser := "Browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/") || strings.Contains(ua, "Opera"):
		browser = "Opera"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "Chromium/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	os := ""
	switch {
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Mac OS X") || strings.Contains(ua, "Macintosh"):
		os = "macOS"
	case strings.Contains(ua, "Linux") || strings.Contains(ua, "X11"):
		os = "Linux"
	}
	if os == "" {
		return browser
	}
	return browser + " on " + os
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func ptr[T any](v T) *T { return &v }
