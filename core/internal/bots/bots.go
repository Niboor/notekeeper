// Package bots manages bot instances and their credentials, authenticates bots, and links chat
// identities to accounts (docs/design/03-auth.md section 5).
package bots

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/auth"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
	"github.com/Niboor/notekeeper/core/internal/throttle"
)

// Scopes of a bot credential (AUTH-B1).
const (
	ScopeIngest  = "ingest"
	ScopeDeliver = "deliver"
)

// Errors returned by the service.
var (
	ErrUnauthenticated = errors.New("unauthenticated bot")
	ErrConflict        = errors.New("conflict")
	ErrInvalidInput    = errors.New("invalid input")
)

// Service manages bots.
type Service struct {
	St  *store.Store
	Now func() time.Time

	throttle *throttle.Throttle
}

// New creates the service.
func New(st *store.Store) *Service {
	s := &Service{St: st, Now: time.Now}
	s.throttle = &throttle.Throttle{St: st, Now: func() time.Time { return s.Now() }}
	return s
}

// Principal is an authenticated bot: its instance and the scopes of the credential it used.
type Principal struct {
	InstanceID     uuid.UUID
	Type           string
	Name           string
	IdentityDomain string
	CredentialID   uuid.UUID
	Scopes         []string
}

// HasScope reports whether the credential carries scope.
func (p *Principal) HasScope(scope string) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// CreateInstanceInput registers a bot instance.
type CreateInstanceInput struct{ Type, Name, IdentityDomain string }

// CreateInstance registers a bot instance (AUTH-B1).
func (s *Service) CreateInstance(ctx context.Context, actor Actor, in CreateInstanceInput) (dbq.BotInstance, error) {
	in.Type, in.Name, in.IdentityDomain = strings.TrimSpace(in.Type), strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.IdentityDomain))
	if in.Type == "" || in.Name == "" || len(in.Name) > 64 || len(in.Type) > 32 {
		return dbq.BotInstance{}, fmt.Errorf("%w: type and name are required", ErrInvalidInput)
	}
	if in.Type == "matrix" && in.IdentityDomain == "" {
		// The domain is what limits which chat identities the bot may assert (SEC-BOT-5): without it a
		// stolen bot key could name any user of any homeserver.
		return dbq.BotInstance{}, fmt.Errorf("%w: a Matrix bot needs the homeserver domain of its users", ErrInvalidInput)
	}
	id, _ := uuid.NewV7()
	var out dbq.BotInstance
	err := s.St.InTx(ctx, func(q *dbq.Queries) error {
		var err error
		out, err = q.CreateBotInstance(ctx, dbq.CreateBotInstanceParams{ID: id, Type: in.Type, Name: in.Name, IdentityDomain: in.IdentityDomain})
		if store.IsUniqueViolation(err, "") {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "bot.instance_created", TargetKind: "bot_instance", TargetID: &id})
	})
	return out, err
}

// Actor is the audit identity of the caller.
type Actor = store.Actor

// ListInstances lists all instances.
func (s *Service) ListInstances(ctx context.Context) ([]dbq.BotInstance, error) {
	return s.St.Q().ListBotInstances(ctx)
}

// SetInstanceStatus enables or disables an instance ("active" or "disabled").
func (s *Service) SetInstanceStatus(ctx context.Context, actor Actor, id uuid.UUID, status string) error {
	if status != "active" && status != "disabled" {
		return fmt.Errorf("%w: status", ErrInvalidInput)
	}
	return s.St.InTx(ctx, func(q *dbq.Queries) error {
		n, err := q.SetBotInstanceStatus(ctx, dbq.SetBotInstanceStatusParams{ID: id, Status: status})
		if err != nil {
			return err
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "bot.instance_" + status, TargetKind: "bot_instance", TargetID: &id})
	})
}

// Credential is a newly created bot credential. Secret is shown once; only its hash is stored (SEC-BOT-8).
type Credential struct {
	ID       uuid.UUID
	ClientID string
	Bearer   string // "nkb.<client id>.<secret>": the value the bot sends
}

// CreateCredential creates a credential for an instance. Several may be active during rotation.
func (s *Service) CreateCredential(ctx context.Context, actor Actor, instance uuid.UUID, scopes []string) (Credential, error) {
	if len(scopes) == 0 {
		scopes = []string{ScopeIngest, ScopeDeliver}
	}
	for _, sc := range scopes {
		if sc != ScopeIngest && sc != ScopeDeliver {
			return Credential{}, fmt.Errorf("%w: unknown scope %q", ErrInvalidInput, sc)
		}
	}
	clientID, secret := auth.RandomToken(12), auth.RandomToken(32) // 256-bit secret
	id, _ := uuid.NewV7()
	err := s.St.InTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.GetBotInstance(ctx, instance); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return store.ErrNotFound
			}
			return err
		}
		if _, err := q.CreateBotCredential(ctx, dbq.CreateBotCredentialParams{
			ID: id, BotInstanceID: instance, ClientID: clientID, SecretHash: auth.HashToken(secret), Scopes: scopes}); err != nil {
			return err
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "bot.credential_created",
			TargetKind: "bot_instance", TargetID: &instance})
	})
	if err != nil {
		return Credential{}, err
	}
	return Credential{ID: id, ClientID: clientID, Bearer: "nkb." + clientID + "." + secret}, nil
}

// DisableCredential disables one credential; it stops working on the next request.
func (s *Service) DisableCredential(ctx context.Context, actor Actor, instance, credential uuid.UUID) error {
	return s.St.InTx(ctx, func(q *dbq.Queries) error {
		now := s.Now()
		n, err := q.DisableBotCredential(ctx, dbq.DisableBotCredentialParams{ID: credential, BotInstanceID: instance, DisabledAt: &now})
		if err != nil {
			return err
		}
		if n == 0 {
			return store.ErrNotFound
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "bot.credential_disabled",
			TargetKind: "bot_instance", TargetID: &instance})
	})
}

// ListCredentials lists an instance's credentials (never the secrets).
func (s *Service) ListCredentials(ctx context.Context, instance uuid.UUID) ([]dbq.ListBotCredentialsRow, error) {
	return s.St.Q().ListBotCredentials(ctx, instance)
}

// dummyHash is compared against when the client id is unknown, so timing reveals nothing.
var dummyHash = auth.HashToken("no such credential")

// Authenticate resolves "nkb.<client id>.<secret>" to a bot principal. The secret is compared
// in constant time against its stored hash; disabled credentials and instances are refused on
// the very next request (SEC-BOT-8).
func (s *Service) Authenticate(ctx context.Context, bearer string) (*Principal, error) {
	rest, ok := strings.CutPrefix(bearer, "nkb.")
	if !ok {
		return nil, ErrUnauthenticated
	}
	clientID, secret, ok := strings.Cut(rest, ".")
	if !ok || clientID == "" || secret == "" || len(bearer) > 200 {
		return nil, ErrUnauthenticated
	}
	row, err := s.St.Q().GetBotCredentialByClientID(ctx, clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		subtle.ConstantTimeCompare(auth.HashToken(secret), dummyHash)
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(auth.HashToken(secret), row.SecretHash) != 1 || row.DisabledAt != nil || row.Status != "active" {
		return nil, ErrUnauthenticated
	}
	return &Principal{InstanceID: row.InstanceID, Type: row.Type, Name: row.Name, IdentityDomain: row.IdentityDomain,
		CredentialID: row.CredentialID, Scopes: row.Scopes}, nil
}

// Heartbeat records that the instance is alive (WEB-12 "bot status"), and the chat address it reports, if
// it belongs to the instance's identity domain: a bot cannot send users to an account elsewhere.
func (s *Service) Heartbeat(ctx context.Context, instance uuid.UUID, identityDomain, address string) error {
	return s.St.Q().TouchBotInstance(ctx, dbq.TouchBotInstanceParams{ID: instance, LastSeenAt: ptr(s.Now()), Address: usableAddress(address, identityDomain)})
}

// usableAddress returns the address when it looks like "<name>:<identity domain>" (as a Matrix user id,
// "@notekeeper:example.org", does) and is short and printable; otherwise nil.
func usableAddress(address, domain string) *string {
	address = strings.TrimSpace(address)
	if address == "" || len(address) > 255 || domain == "" {
		return nil
	}
	for _, r := range address {
		if r <= ' ' || r == 0x7f {
			return nil
		}
	}
	local, host, found := strings.Cut(address, ":")
	if !found || strings.TrimPrefix(local, "@") == "" || !strings.EqualFold(host, domain) {
		return nil
	}
	return &address
}

func ptr[T any](v T) *T { return &v }
