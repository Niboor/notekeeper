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

// The policies of the login throttle. One attempt is charged to three keys (docs/design/03 section 3):
//
//   - Pair is one account from one address: strict, so a person mistyping a password is slowed at once,
//     and an outsider who keeps guessing a known username only ever slows down their own address.
//   - Account is the account from anywhere: many free attempts, and a short ceiling, so guessing spread
//     over many addresses is slowed while nobody can keep the real user out for long.
//   - IP is one address across all accounts: lenient, because several people may share an address.
var (
	Pair    = Policy{Free: 0, Max: 15 * time.Minute}
	Account = Policy{Free: 50, Max: 30 * time.Second}
	IP      = Policy{Free: 20, Max: 15 * time.Minute}
	// Activation is one address using activation links: a legitimate person needs a few tries at most.
	Activation = Policy{Free: 10, Max: 15 * time.Minute}
	// Secret is for a single credential that must never be guessed quickly, such as a pairing code
	// entered by a chat identity or the current password of a signed-in user.
	Secret = Policy{Free: 0, Max: 15 * time.Minute}
)

// Throttle reads and writes throttle rows.
type Throttle struct {
	St  *store.Store
	Now func() time.Time
}

// Charge is one key an attempt is counted against.
type Charge struct {
	Key    string
	Policy Policy
}

// Attempt counts an attempt against every key before the secret is checked, and reports how long the
// caller must wait if any of them is blocked (then the attempt is refused and does not count again on
// that key). Counting first, in one locked step per key, is what makes the limit hold for requests that
// arrive at the same moment: the check and the count cannot be separated by another request (SR-001).
// Call Success when the secret was right; a wrong secret needs no further call.
func (t *Throttle) Attempt(ctx context.Context, charges ...Charge) (time.Duration, error) {
	// A key that is blocked already refuses the attempt at no cost to anyone: nothing is counted, so
	// retrying while blocked does not make the wait longer (a cheap read, not the locked step below).
	keys := make([]string, len(charges))
	for i, c := range charges {
		keys[i] = c.Key
	}
	if wait, err := t.Blocked(ctx, keys...); err != nil || wait > 0 {
		return wait, err
	}
	for _, c := range charges {
		wait, err := t.charge(ctx, c)
		if err != nil || wait > 0 { // blocked between the read and the count: stop, later keys stay untouched
			return wait, err
		}
	}
	return 0, nil
}

func (t *Throttle) charge(ctx context.Context, c Charge) (time.Duration, error) {
	now := t.Now()
	var wait time.Duration
	err := t.St.InTx(ctx, func(q *dbq.Queries) error {
		if err := q.EnsureThrottle(ctx, dbq.EnsureThrottleParams{Key: c.Key, UpdatedAt: now}); err != nil {
			return err
		}
		row, err := q.LockThrottle(ctx, c.Key)
		if err != nil {
			return err
		}
		if row.BlockedUntil != nil && row.BlockedUntil.After(now) {
			wait = row.BlockedUntil.Sub(now)
			return nil
		}
		n := int(row.Failures) + 1
		if row.UpdatedAt.Before(now.Add(-DecayAfter)) {
			n = 1
		}
		var until *time.Time
		if d := c.Policy.Delay(n); d > 0 {
			until = ptr(now.Add(d))
		}
		return q.SetThrottle(ctx, dbq.SetThrottleParams{Key: c.Key, Failures: int32(n), BlockedUntil: until, UpdatedAt: now})
	})
	return wait, err
}

// Success takes back the charge of an attempt whose secret was right. Keys named in reset are
// forgotten altogether; the others lose one failure (so a stranger's successful sign-in cannot wipe
// what a guesser on the same address or account has piled up).
func (t *Throttle) Success(ctx context.Context, reset []string, refund ...Charge) error {
	for _, k := range reset {
		if err := t.St.Q().ResetThrottle(ctx, k); err != nil {
			return err
		}
	}
	for _, c := range refund {
		err := t.St.InTx(ctx, func(q *dbq.Queries) error {
			row, err := q.LockThrottle(ctx, c.Key)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			n := max(int(row.Failures)-1, 0)
			if n == 0 {
				return q.ResetThrottle(ctx, c.Key)
			}
			until := row.BlockedUntil
			if n <= c.Policy.Free {
				until = nil
			}
			return q.SetThrottle(ctx, dbq.SetThrottleParams{Key: c.Key, Failures: int32(n), BlockedUntil: until, UpdatedAt: row.UpdatedAt})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Blocked returns the time still to wait for the first blocked key, or 0. It does not count anything;
// it is for checks that are not attempts.
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
			wait = max(wait, row.BlockedUntil.Sub(now))
		}
	}
	return wait, nil
}

// Fail records a failure for key and blocks it for the delay the policy prescribes. It is for
// checks that are separate from the attempt (see Attempt for logins).
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
