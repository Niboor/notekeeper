package timeparse

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Rule is the recurrence of a reminder: a small subset of RFC 5545 (CORE-R10). It is stored as
// text, for example "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE".
type Rule struct {
	Freq     string // DAILY, WEEKLY or MONTHLY
	Interval int
	ByDay    []time.Weekday // WEEKLY only
}

var dayCodes = map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}

// ParseRule reads the text form. Unknown parts are refused rather than ignored, so a rule always means what it says.
func ParseRule(s string) (Rule, error) {
	r := Rule{Interval: 1}
	for _, part := range strings.Split(strings.TrimSpace(s), ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return Rule{}, fmt.Errorf("invalid recurrence %q", s)
		}
		switch strings.ToUpper(k) {
		case "FREQ":
			r.Freq = strings.ToUpper(v)
			if r.Freq != "DAILY" && r.Freq != "WEEKLY" && r.Freq != "MONTHLY" {
				return Rule{}, fmt.Errorf("unsupported frequency %q", v)
			}
		case "INTERVAL":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 366 {
				return Rule{}, fmt.Errorf("invalid interval %q", v)
			}
			r.Interval = n
		case "BYDAY":
			for _, d := range strings.Split(v, ",") {
				wd, ok := dayCodes[strings.ToUpper(d)]
				if !ok {
					return Rule{}, fmt.Errorf("invalid weekday %q", d)
				}
				r.ByDay = append(r.ByDay, wd)
			}
		default:
			return Rule{}, fmt.Errorf("unsupported recurrence part %q", k)
		}
	}
	if r.Freq == "" {
		return Rule{}, fmt.Errorf("recurrence needs FREQ")
	}
	if len(r.ByDay) > 0 && r.Freq != "WEEKLY" {
		return Rule{}, fmt.Errorf("BYDAY needs FREQ=WEEKLY")
	}
	return r, nil
}

// Next returns the first occurrence strictly after `after`, counting from the occurrence `from`.
// Occurrences keep the local time of day of `from` in loc, so "09:00" stays 09:00 across daylight
// saving changes. Occurrences missed while nothing was running collapse: only the first one after
// `after` is returned (CORE-R10).
func (r Rule) Next(from, after time.Time, loc *time.Location) time.Time {
	f := from.In(loc)
	h, m := f.Hour(), f.Minute()
	at := func(y int, mo time.Month, d int) time.Time { return time.Date(y, mo, d, h, m, 0, 0, loc) }
	anchorDay := f.Day()
	weekStart := func(t time.Time) time.Time { // Monday of t's week, at midnight
		d := (int(t.Weekday()) + 6) % 7
		return time.Date(t.Year(), t.Month(), t.Day()-d, 0, 0, 0, 0, loc)
	}
	anchorWeek := weekStart(f)
	cur := f
	for i := 0; i < 20000; i++ {
		var next time.Time
		switch r.Freq {
		case "DAILY":
			next = at(cur.Year(), cur.Month(), cur.Day()+r.Interval)
		case "MONTHLY":
			y, mo := cur.Year(), int(cur.Month())+r.Interval
			for mo > 12 {
				mo -= 12
				y++
			}
			day := anchorDay
			if last := time.Date(y, time.Month(mo)+1, 0, 0, 0, 0, 0, loc).Day(); day > last {
				day = last // 31 January is followed by the last day of February
			}
			next = at(y, time.Month(mo), day)
		default: // WEEKLY
			days := r.ByDay
			if len(days) == 0 {
				days = []time.Weekday{f.Weekday()}
			}
			next = time.Time{}
			for d := 1; d <= 7*r.Interval+7; d++ {
				c := at(cur.Year(), cur.Month(), cur.Day()+d)
				weeks := int(weekStart(c).Sub(anchorWeek).Round(24*time.Hour).Hours() / (24 * 7))
				if weeks%r.Interval != 0 {
					continue
				}
				for _, wd := range days {
					if c.Weekday() == wd {
						next = c
					}
				}
				if !next.IsZero() {
					break
				}
			}
		}
		if next.IsZero() {
			return time.Time{}
		}
		if next.After(after) {
			return next
		}
		cur = next
	}
	return time.Time{}
}
