// Package throttle slows down repeated failures (login, pairing codes) with state kept in
// PostgreSQL, so the limit holds across replicas (docs/design/03-auth.md section 3, SEC-AUTH-2).
// It only ever delays: there is never a permanent lock-out.
package throttle

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// Policy computes the delay after n consecutive failures.
type Policy struct {
	// Free failures are allowed before any delay applies.
	Free int
	Max  time.Duration
}

// Delay after n consecutive failures: 2^(n-Free) seconds, capped at Max.
func (p Policy) Delay(n int) time.Duration {
	over := n - p.Free
	if over <= 0 {
		return 0
	}
	if over > 20 {
		return p.Max
	}
	d := time.Duration(1<<over) * time.Second
	if d > p.Max {
		return p.Max
	}
	return d
}

// DecayAfter is how long without a failure resets a counter.
const DecayAfter = 15 * time.Minute

// Account is the policy for a single account or identity: the second failure in a row already waits.
var Account = Policy{Free: 0, Max: 15 * time.Minute}

// IP is more lenient because several people may share an address.
var IP = Policy{Free: 5, Max: 15 * time.Minute}

// Throttle reads and writes throttle rows.
type Throttle struct {
	St  *store.Store
	Now func() time.Time
}

// Blocked returns the time still to wait for the first blocked key, or 0.
func (t *Throttle) Blocked(ctx context.Context, keys ...string) (time.Duration, error) {
	now := t.Now()
	var wait time.Duration
	for _, k := range keys {
		row, err := t.St.Q().GetThrottle(ctx, k)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if row.BlockedUntil != nil && row.BlockedUntil.After(now) {
			if d := row.BlockedUntil.Sub(now); d > wait {
				wait = d
			}
		}
	}
	return wait, nil
}

// Fail records a failure for key and blocks it for the delay the policy prescribes.
func (t *Throttle) Fail(ctx context.Context, key string, p Policy) error {
	now := t.Now()
	return t.St.InTx(ctx, func(q *dbq.Queries) error {
		n, err := q.RecordThrottleFailure(ctx, dbq.RecordThrottleFailureParams{
			Key: key, UpdatedAt: now, UpdatedAt_2: now.Add(-DecayAfter),
		})
		if err != nil {
			return err
		}
		if d := p.Delay(int(n)); d > 0 {
			return q.SetThrottleBlock(ctx, dbq.SetThrottleBlockParams{Key: key, BlockedUntil: ptr(now.Add(d))})
		}
		return nil
	})
}

// Reset forgets the failures of key (after a success).
func (t *Throttle) Reset(ctx context.Context, key string) error {
	return t.St.Q().ResetThrottle(ctx, key)
}

func ptr[T any](v T) *T { return &v }
