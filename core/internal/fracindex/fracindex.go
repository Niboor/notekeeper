// Package fracindex generates fractional position keys: base-62 strings ordered by plain byte
// comparison (PostgreSQL collation "C"), with a key producible strictly between any two others
// (docs/design/01-data-model.md section 7). Moving or inserting one note writes only that note's
// key, so two devices reordering different notes never conflict.
//
// Keys never end in the smallest digit '0', so there is always room below any key.
package fracindex

import (
	"errors"
	"strings"
)

// digits are in ascending byte order, which is what makes string comparison match numeric order.
const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// MaxLength is the key length above which the server rebalances a category (design 01 section 7).
const MaxLength = 48

// ErrOrder is returned when a is not strictly below b.
var ErrOrder = errors.New("fracindex: lower key must sort before upper key")

// ErrInvalid is returned for a key that contains a character outside the alphabet or ends in '0'.
var ErrInvalid = errors.New("fracindex: invalid key")

// Valid reports whether k is a well-formed key.
func Valid(k string) bool {
	if k == "" || k[len(k)-1] == '0' {
		return false
	}
	for i := range len(k) {
		if strings.IndexByte(digits, k[i]) < 0 {
			return false
		}
	}
	return true
}

func digit(k string, i int) int {
	if i >= len(k) {
		return 0
	}
	return strings.IndexByte(digits, k[i])
}

// Between returns a key strictly between a and b. An empty a means "before everything", an empty
// b means "after everything"; both empty returns the middle of the space.
func Between(a, b string) (string, error) {
	if (a != "" && !Valid(a)) || (b != "" && !Valid(b)) {
		return "", ErrInvalid
	}
	if b != "" && a >= b {
		return "", ErrOrder
	}
	return midpoint(a, b), nil
}

// midpoint assumes a < b (or b == "" meaning infinity) and that both are valid.
func midpoint(a, b string) string {
	// Appending and prepending are by far the commonest operations (new notes go to the end or
	// the top of a column), so they step to the neighbouring digit instead of bisecting: keys then
	// grow by one character per 62 operations instead of one per five.
	if b == "" && a != "" {
		if i := digit(a, 0); i < len(digits)-1 {
			return string(digits[i+1])
		}
		return a[:1] + midpoint(sliceFrom(a, 1), "")
	}
	if a == "" && b != "" {
		switch i := digit(b, 0); {
		case i >= 2:
			return string(digits[i-1])
		case len(b) > 1 && i == 0:
			return "0" + midpoint("", b[1:])
		default: // b starts with '1' (or is "0"-prefixed and short): go below it with the middle digit
			return "0V"
		}
	}
	if b != "" {
		// Skip the common prefix; a is padded with the smallest digit past its end.
		n := 0
		for n < len(b) && digit(a, n) == strings.IndexByte(digits, b[n]) {
			n++
		}
		if n > 0 {
			return b[:n] + midpoint(sliceFrom(a, n), b[n:])
		}
	}
	da := digit(a, 0)
	db := len(digits)
	if b != "" {
		db = strings.IndexByte(digits, b[0])
	}
	if db-da > 1 {
		return string(digits[(da+db+1)/2])
	}
	// The first digits are consecutive.
	if len(b) > 1 {
		return b[:1] // longer than a's one digit but still above it, and below b
	}
	return string(digits[da]) + midpoint(sliceFrom(a, 1), "")
}

func sliceFrom(s string, n int) string {
	if n >= len(s) {
		return ""
	}
	return s[n:]
}

// Even returns n strictly increasing keys spread evenly over the space, as short as the count
// allows. It is used to rebalance a category whose keys have grown long.
func Even(n int) []string {
	if n <= 0 {
		return nil
	}
	width := 1
	for pow(len(digits), width) < n+1 {
		width++
	}
	width++ // headroom so later insertions between neighbours stay short
	total := pow(len(digits), width)
	step := total / (n + 1)
	out := make([]string, n)
	for i := range n {
		out[i] = encode((i+1)*step, width)
		if out[i][len(out[i])-1] == '0' {
			out[i] += "V" // keep the "never ends in 0" invariant; order is unaffected
		}
	}
	return out
}

func pow(base, exp int) int {
	r := 1
	for range exp {
		r *= base
	}
	return r
}

func encode(v, width int) string {
	buf := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		buf[i] = digits[v%len(digits)]
		v /= len(digits)
	}
	return string(buf)
}
