// Package position turns "put this between those two" requests into fractional keys
// (docs/design/01-data-model.md section 7). Clients send the ids of the neighbours they dropped
// between; the server reads their keys under the user lock and generates the new key, so clients
// never generate keys and two devices reordering different items never conflict.
package position

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/fracindex"
)

// ErrNeighbour is returned when a neighbour id is not a member of the group (foreign or missing
// ids look the same, SEC-ISO-3, SEC-ISO-4).
var ErrNeighbour = errors.New("position: neighbour is not in this list")

// Group describes one ordered list (the pages of a user, the categories of a page, the active
// notes of a category) through the few questions placement needs. The item being placed, if it
// is already in the list, must be excluded from every answer.
type Group struct {
	// KeyOf returns the key of a member, ok=false if id is not a member.
	KeyOf func(ctx context.Context, id uuid.UUID) (key string, ok bool, err error)
	// After returns the smallest key greater than key, if any.
	After func(ctx context.Context, key string) (string, bool, error)
	// Before returns the greatest key smaller than key, if any.
	Before func(ctx context.Context, key string) (string, bool, error)
	First  func(ctx context.Context) (string, bool, error)
	Last   func(ctx context.Context) (string, bool, error)
	// Rebalance rewrites every member's key with short evenly spaced keys (the placed item is not
	// a member yet or is excluded) and notifies clients of the changes.
	Rebalance func(ctx context.Context) error
}

// Hints say where the item should go. AfterID is the item that should come right before it,
// BeforeID the one that should come right after.
type Hints struct {
	AfterID, BeforeID *uuid.UUID
	// DefaultTop chooses the top of the list when there are no hints (new notes go to the top of a
	// column); otherwise the end (new pages and categories).
	DefaultTop bool
}

// Place returns a key for the item. If the new key would exceed fracindex.MaxLength the group is
// rebalanced and the placement is computed again against the short keys.
func (g Group) Place(ctx context.Context, h Hints) (string, error) {
	key, err := g.place(ctx, h)
	if err != nil {
		return "", err
	}
	if len(key) <= fracindex.MaxLength {
		return key, nil
	}
	if err := g.Rebalance(ctx); err != nil {
		return "", fmt.Errorf("rebalance: %w", err)
	}
	if key, err = g.place(ctx, h); err != nil {
		return "", err
	}
	return key, nil
}

func (g Group) place(ctx context.Context, h Hints) (string, error) {
	var lo, hi string // the bounds of the gap; "" means open
	switch {
	case h.AfterID != nil && h.BeforeID != nil:
		a, ok, err := g.KeyOf(ctx, *h.AfterID)
		if err != nil || !ok {
			return "", orNeighbour(err)
		}
		b, ok, err := g.KeyOf(ctx, *h.BeforeID)
		if err != nil || !ok {
			return "", orNeighbour(err)
		}
		if a < b { // the neighbours are still in the order the client saw
			lo, hi = a, b
			break
		}
		// They moved in the meantime; honour "right after AfterID".
		lo = a
		hi, _, err = g.After(ctx, a)
		if err != nil {
			return "", err
		}
	case h.AfterID != nil:
		a, ok, err := g.KeyOf(ctx, *h.AfterID)
		if err != nil || !ok {
			return "", orNeighbour(err)
		}
		lo = a
		if hi, _, err = g.After(ctx, a); err != nil {
			return "", err
		}
	case h.BeforeID != nil:
		b, ok, err := g.KeyOf(ctx, *h.BeforeID)
		if err != nil || !ok {
			return "", orNeighbour(err)
		}
		hi = b
		if lo, _, err = g.Before(ctx, b); err != nil {
			return "", err
		}
	case h.DefaultTop:
		var err error
		if hi, _, err = g.First(ctx); err != nil {
			return "", err
		}
	default:
		var err error
		if lo, _, err = g.Last(ctx); err != nil {
			return "", err
		}
	}
	return fracindex.Between(lo, hi)
}

func orNeighbour(err error) error {
	if err != nil {
		return err
	}
	return ErrNeighbour
}
