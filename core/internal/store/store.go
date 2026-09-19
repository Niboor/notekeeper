// Package store is the only way the application talks to PostgreSQL. Its central piece is
// InUserTx, the single chokepoint for writes to user-owned data: it locks the user's row,
// scopes the transaction to that user for row-level security, hands out gapless change-feed
// sequence numbers and notifies listeners on commit (docs/design/README.md section 3).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// Notification channels used with LISTEN/NOTIFY (docs/design/05-realtime-and-jobs.md section 1).
const (
	ChannelChanges  = "nk_changes"  // payload "<user id>:<seq>"
	ChannelSessions = "nk_sessions" // payload "<session id>"
	ChannelOutbox   = "nk_outbox"   // payload "<bot instance id>"
)

// ErrNotFound is returned when a row does not exist, or is not visible to the acting user.
// Foreign and missing ids are deliberately indistinguishable (SEC-ISO-3).
var ErrNotFound = errors.New("not found")

// Store wraps the connection pool.
type Store struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
}

// New wraps pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool, q: dbq.New(pool)} }

// Pool exposes the pool for the few callers (readiness, workers) that need it directly.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Q returns queries bound to the pool. Because they run outside any user context, row-level
// security hides every content row from them; they are for the tables that locate a user
// (users, sessions, bots, identities) and for operational tables.
func (s *Store) Q() *dbq.Queries { return s.q }

// InTx runs fn in a plain transaction without a user context. It is for account, session and
// bot administration, which touch only tables outside row-level security.
func (s *Store) InTx(ctx context.Context, fn func(q *dbq.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(dbq.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UserTx is a transaction acting for one user. Obtain it only through InUserTx.
type UserTx struct {
	Tx     pgx.Tx
	Q      *dbq.Queries
	UserID uuid.UUID
	// Status is the user's status when the transaction started.
	Status string

	lastSeq int64
}

// InUserTx runs fn in a transaction that
//
//  1. locks the user's row (per-user write serialisation: gapless change feed, serial grouping),
//  2. sets app.user_id so row-level security scopes every content query to the user,
//  3. lets fn record changes with Change, and
//  4. notifies listeners on commit, with identifiers only (never content).
//
// It returns ErrNotFound when the user does not exist.
func (s *Store) InUserTx(ctx context.Context, userID uuid.UUID, fn func(tx *UserTx) error) error {
	pgtx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = pgtx.Rollback(ctx) }()

	if _, err := pgtx.Exec(ctx, `select set_config('app.user_id', $1, true)`, userID.String()); err != nil {
		return fmt.Errorf("set user context: %w", err)
	}
	q := dbq.New(pgtx)
	u, err := q.LockUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock user: %w", err)
	}
	tx := &UserTx{Tx: pgtx, Q: q, UserID: userID, Status: u.Status}
	if err := fn(tx); err != nil {
		return err
	}
	if tx.lastSeq > 0 {
		payload := userID.String() + ":" + strconv.FormatInt(tx.lastSeq, 10)
		if _, err := pgtx.Exec(ctx, `select pg_notify($1, $2)`, ChannelChanges, payload); err != nil {
			return fmt.Errorf("notify: %w", err)
		}
	}
	return pgtx.Commit(ctx)
}

// Change appends an entry to the user's change feed with the next sequence number. Sequence
// numbers are gapless and in commit order because the user's row is locked for the whole
// transaction (CORE-S3). op is "upsert" or "delete"; version may be nil for deletions.
func (t *UserTx) Change(ctx context.Context, entityType string, entityID uuid.UUID, op string, version *int32) error {
	seq, err := t.Q.NextChangeSeq(ctx, t.UserID)
	if err != nil {
		return fmt.Errorf("next change seq: %w", err)
	}
	t.lastSeq = seq
	return t.Q.InsertChange(ctx, dbq.InsertChangeParams{
		UserID: t.UserID, Seq: seq, EntityType: entityType, EntityID: entityID, Op: op, Version: version,
	})
}

// Notify sends a notification on channel at commit time. Payloads carry identifiers only.
func (t *UserTx) Notify(ctx context.Context, channel, payload string) error {
	_, err := t.Tx.Exec(ctx, `select pg_notify($1, $2)`, channel, payload)
	return err
}

// Notify sends a notification outside a user transaction (immediately).
func (s *Store) Notify(ctx context.Context, channel, payload string) error {
	_, err := s.pool.Exec(ctx, `select pg_notify($1, $2)`, channel, payload)
	return err
}

// IsUniqueViolation reports whether err is a PostgreSQL unique-constraint violation, and
// optionally on the named constraint.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return constraint == "" || pgErr.ConstraintName == constraint
	}
	return false
}

// Actor identifies who performed an action, for the audit log.
type Actor struct {
	Kind string // user | admin | bot | system | anonymous
	ID   *uuid.UUID
}

// AuditEntry is one append-only audit record (SEC-AUD-1). Detail holds small structured facts
// and never content, tokens or filenames (SEC-AUD-2).
type AuditEntry struct {
	ActorKind  string // user | admin | bot | system | anonymous
	ActorID    *uuid.UUID
	Action     string
	TargetKind string
	TargetID   *uuid.UUID
	Detail     map[string]any
}

// Audit appends e to the audit log using q (so it commits with the change it describes).
func Audit(ctx context.Context, q *dbq.Queries, e AuditEntry) error {
	detail := []byte("{}")
	if len(e.Detail) > 0 {
		var err error
		if detail, err = json.Marshal(e.Detail); err != nil {
			return err
		}
	}
	var target *string
	if e.TargetKind != "" {
		target = &e.TargetKind
	}
	return q.InsertAudit(ctx, dbq.InsertAuditParams{
		ActorKind: e.ActorKind, ActorID: uuidPtr(e.ActorID), Action: e.Action,
		TargetKind: target, TargetID: uuidPtr(e.TargetID), Detail: detail,
	})
}

func uuidPtr(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

// InUserRead runs fn in a read-only transaction scoped to the user through row-level security.
// It takes no lock and records no changes, so reads never queue behind writers.
func (s *Store) InUserRead(ctx context.Context, userID uuid.UUID, fn func(q *dbq.Queries) error) error {
	pgtx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = pgtx.Rollback(ctx) }()
	if _, err := pgtx.Exec(ctx, `select set_config('app.user_id', $1, true)`, userID.String()); err != nil {
		return fmt.Errorf("set user context: %w", err)
	}
	if err := fn(dbq.New(pgtx)); err != nil {
		return err
	}
	return pgtx.Commit(ctx)
}
