package server

import "testing"

// A client that cannot be caught up from the change feed is told to refetch (CR-040, CORE-N14b).
func TestChangesLost(t *testing.T) {
	for _, tc := range []struct {
		name              string
		last, oldest, cur int64
		want              bool
	}{
		{"up to date", 10, 5, 10, false},
		{"behind but the feed still has it", 8, 5, 10, false},
		{"exactly at the edge", 4, 5, 10, false},
		{"missed purged changes", 3, 5, 10, true},
		{"everything purged after quiet weeks", 3, 0, 10, true},
		{"nothing ever happened", 0, 0, 0, false},
		{"client ahead of a restored database", 12, 5, 10, true},
	} {
		if got := changesLost(tc.last, tc.oldest, tc.cur); got != tc.want {
			t.Errorf("%s: changesLost(%d,%d,%d) = %v", tc.name, tc.last, tc.oldest, tc.cur, got)
		}
	}
}
