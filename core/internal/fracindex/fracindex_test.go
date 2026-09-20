package fracindex

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"slices"
	"sort"
	"testing"
)

func TestSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Between []struct{ A, B, Want string } `json:"between"`
		Invalid []string                      `json:"invalid"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Between {
		got, err := Between(c.A, c.B)
		if err != nil || got != c.Want {
			t.Errorf("Between(%q, %q) = %q, %v; want %q", c.A, c.B, got, err, c.Want)
		}
	}
	for _, k := range v.Invalid {
		if Valid(k) {
			t.Errorf("Valid(%q) = true", k)
		}
		if _, err := Between(k, ""); err == nil {
			t.Errorf("Between(%q, \"\") accepted an invalid key", k)
		}
	}
}

func TestBetweenRejectsBadOrder(t *testing.T) {
	for _, c := range [][2]string{{"b", "a"}, {"a", "a"}} {
		if _, err := Between(c[0], c[1]); err == nil {
			t.Errorf("Between(%q, %q) should fail", c[0], c[1])
		}
	}
}

// Random insertions keep the list strictly sorted, every key valid, whatever the pattern.
func TestRandomInsertionsStaySorted(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	keys := []string{}
	for range 4000 {
		i := rng.IntN(len(keys) + 1) // insert at position i
		var a, b string
		if i > 0 {
			a = keys[i-1]
		}
		if i < len(keys) {
			b = keys[i]
		}
		k, err := Between(a, b)
		if err != nil {
			t.Fatalf("Between(%q, %q): %v", a, b, err)
		}
		if !Valid(k) || (a != "" && k <= a) || (b != "" && k >= b) {
			t.Fatalf("Between(%q, %q) = %q is not strictly between", a, b, k)
		}
		keys = slices.Insert(keys, i, k)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatal("keys not sorted")
	}
}

// Appending at either end for a long time keeps keys short, and the worst case (always inserting
// at the same spot) grows slowly enough for the rebalance threshold to be reached rarely.
func TestGrowthAtTheEndsAndInTheMiddle(t *testing.T) {
	for name, pick := range map[string]func(keys []string) (string, string){
		"append":                func(k []string) (string, string) { return k[len(k)-1], "" },
		"prepend":               func(k []string) (string, string) { return "", k[0] },
		"between the first two": func(k []string) (string, string) { return k[0], k[1] },
	} {
		keys := []string{"V", "W"}
		longest := 0
		for range 500 {
			a, b := pick(keys)
			k, err := Between(a, b)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "append":
				keys = append(keys, k)
			case "prepend":
				keys = append([]string{k}, keys...)
			default:
				keys = slices.Insert(keys, 1, k)
			}
			longest = max(longest, len(k))
		}
		t.Logf("%s: longest key after 500 insertions: %d", name, longest)
		if name != "between the first two" && longest > 100 {
			t.Errorf("%s: keys grew to %d characters", name, longest)
		}
		if !sort.StringsAreSorted(keys) {
			t.Errorf("%s: unsorted", name)
		}
	}
}

func TestEvenKeysAreSortedShortAndLeaveRoom(t *testing.T) {
	for _, n := range []int{1, 2, 10, 61, 62, 63, 500, 5000} {
		keys := Even(n)
		if len(keys) != n || !sort.StringsAreSorted(keys) {
			t.Fatalf("Even(%d): %d keys, sorted=%v", n, len(keys), sort.StringsAreSorted(keys))
		}
		for i, k := range keys {
			if !Valid(k) || (i > 0 && k <= keys[i-1]) {
				t.Fatalf("Even(%d)[%d] = %q", n, i, k)
			}
			if len(k) > 6 {
				t.Fatalf("Even(%d) produced a long key %q", n, k)
			}
		}
		// There is room to insert before, between and after.
		for _, pair := range [][2]string{{"", keys[0]}, {keys[n-1], ""}} {
			if k, err := Between(pair[0], pair[1]); err != nil || len(k) > 8 {
				t.Fatalf("no short room next to Even(%d): %q %v", n, k, err)
			}
		}
	}
	if Even(0) != nil {
		t.Fatal("Even(0)")
	}
}
