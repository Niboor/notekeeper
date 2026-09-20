package position

import (
	"context"
	"errors"
	"slices"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/fracindex"
)

// memGroup is an in-memory ordered list, so placement can be tested without a database.
type memGroup struct {
	ids  []uuid.UUID
	keys map[uuid.UUID]string
	reb  int
}

func newMem(n int) *memGroup {
	m := &memGroup{keys: map[uuid.UUID]string{}}
	for i, k := range fracindex.Even(n) {
		id := uuid.New()
		m.ids = append(m.ids, id)
		m.keys[id] = k
		_ = i
	}
	return m
}

func (m *memGroup) sorted() []string {
	var out []string
	for _, k := range m.keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (m *memGroup) group() Group {
	return Group{
		KeyOf: func(_ context.Context, id uuid.UUID) (string, bool, error) { k, ok := m.keys[id]; return k, ok, nil },
		After: func(_ context.Context, key string) (string, bool, error) {
			for _, k := range m.sorted() {
				if k > key {
					return k, true, nil
				}
			}
			return "", false, nil
		},
		Before: func(_ context.Context, key string) (string, bool, error) {
			s := m.sorted()
			for i := len(s) - 1; i >= 0; i-- {
				if s[i] < key {
					return s[i], true, nil
				}
			}
			return "", false, nil
		},
		First: func(context.Context) (string, bool, error) {
			s := m.sorted()
			if len(s) == 0 {
				return "", false, nil
			}
			return s[0], true, nil
		},
		Last: func(context.Context) (string, bool, error) {
			s := m.sorted()
			if len(s) == 0 {
				return "", false, nil
			}
			return s[len(s)-1], true, nil
		},
		Rebalance: func(context.Context) error {
			m.reb++
			order := slices.SortedFunc(func(yield func(uuid.UUID) bool) {
				for id := range m.keys {
					if !yield(id) {
						return
					}
				}
			}, func(a, b uuid.UUID) int {
				switch {
				case m.keys[a] < m.keys[b]:
					return -1
				case m.keys[a] > m.keys[b]:
					return 1
				}
				return 0
			})
			for i, k := range fracindex.Even(len(order)) {
				m.keys[order[i]] = k
			}
			return nil
		},
	}
}

func ptr(id uuid.UUID) *uuid.UUID { return &id }

func TestPlacementRules(t *testing.T) {
	ctx := context.Background()
	m := newMem(3)
	g := m.group()
	a, b, c := m.ids[0], m.ids[1], m.ids[2]

	between := func(after, before *uuid.UUID, top bool) string {
		k, err := g.Place(ctx, Hints{AfterID: after, BeforeID: before, DefaultTop: top})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	// Between two neighbours.
	if k := between(ptr(a), ptr(b), false); !(k > m.keys[a] && k < m.keys[b]) {
		t.Fatalf("between a and b: %q", k)
	}
	// Right after a single neighbour: before its successor, not at the end.
	if k := between(ptr(a), nil, false); !(k > m.keys[a] && k < m.keys[b]) {
		t.Fatalf("after a: %q", k)
	}
	// Right before a single neighbour: after its predecessor.
	if k := between(nil, ptr(c), false); !(k > m.keys[b] && k < m.keys[c]) {
		t.Fatalf("before c: %q", k)
	}
	// No hints: top or end.
	if k := between(nil, nil, true); k >= m.keys[a] {
		t.Fatalf("top: %q", k)
	}
	if k := between(nil, nil, false); k <= m.keys[c] {
		t.Fatalf("end: %q", k)
	}
	// Stale hints (the neighbours swapped since the client looked) still land next to AfterID.
	if k := between(ptr(b), ptr(a), false); !(k > m.keys[b] && k < m.keys[c]) {
		t.Fatalf("stale neighbours: %q", k)
	}
	// An empty list works.
	empty := newMem(0)
	if k, err := empty.group().Place(ctx, Hints{DefaultTop: true}); err != nil || k == "" {
		t.Fatalf("empty list: %q %v", k, err)
	}
	// A foreign or missing neighbour is refused.
	if _, err := g.Place(ctx, Hints{AfterID: ptr(uuid.New())}); !errors.Is(err, ErrNeighbour) {
		t.Fatalf("unknown neighbour: %v", err)
	}
}

// Repeatedly inserting at the same spot eventually triggers a rebalance, after which keys are
// short again and the order of the existing items is unchanged.
func TestRebalanceKeepsOrderAndShortensKeys(t *testing.T) {
	ctx := context.Background()
	m := newMem(5)
	g := m.group()
	first := m.ids[0]
	orderBefore := slices.Clone(m.ids)
	for range 2500 { // always "right after the first item": the worst case for key growth
		k, err := g.Place(ctx, Hints{AfterID: ptr(first)})
		if err != nil {
			t.Fatal(err)
		}
		if len(k) > fracindex.MaxLength {
			t.Fatalf("a key of %d characters escaped the rebalance", len(k))
		}
		id := uuid.New()
		m.keys[id] = k
		m.ids = append(m.ids, id)
	}
	if m.reb == 0 {
		t.Fatal("no rebalance happened in 2500 worst-case insertions")
	}
	// The original five still sort in their original relative order.
	sort.SliceStable(orderBefore, func(i, j int) bool { return m.keys[orderBefore[i]] < m.keys[orderBefore[j]] })
	if !slices.Equal(orderBefore, m.ids[:5]) {
		t.Fatal("rebalancing reordered existing items")
	}
	for id, k := range m.keys {
		if !fracindex.Valid(k) {
			t.Fatalf("invalid key %q for %s", k, id)
		}
	}
}
