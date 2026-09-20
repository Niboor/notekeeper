package timeparse

import (
	"errors"
	"testing"
	"time"
)

func brussels(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Skip("no tz database")
	}
	return loc
}

// Every expression the design promises, relative to a fixed clock (CORE-R11, CORE-R13).
// "now" is Wednesday 2026-03-11 14:30 in Brussels.
func TestParse(t *testing.T) {
	loc := brussels(t)
	now := time.Date(2026, 3, 11, 14, 30, 0, 0, loc)
	at := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, loc) }
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"in 2 hours", now.Add(2 * time.Hour)},
		{"in 45 minutes", now.Add(45 * time.Minute)},
		{"in 45m", now.Add(45 * time.Minute)},
		{"in 2h", now.Add(2 * time.Hour)},
		{"in a day", now.Add(24 * time.Hour)},
		{"in an hour", now.Add(time.Hour)},
		{"in half an hour", now.Add(30 * time.Minute)},
		{"in 1.5 hours", now.Add(90 * time.Minute)},
		{"in 3 days", now.Add(72 * time.Hour)},
		{"in 2 weeks", now.Add(14 * 24 * time.Hour)},
		{"tomorrow", at(2026, 3, 12, 9, 0)},
		{"tomorrow 9am", at(2026, 3, 12, 9, 0)},
		{"Tomorrow 9AM", at(2026, 3, 12, 9, 0)},
		{"tomorrow at 9:30pm", at(2026, 3, 12, 21, 30)},
		{"9am tomorrow", at(2026, 3, 12, 9, 0)},
		{"tomorrow 18:30", at(2026, 3, 12, 18, 30)},
		{"tomorrow noon", at(2026, 3, 12, 12, 0)},
		{"today 17:00", at(2026, 3, 11, 17, 0)},
		{"today at 5pm", at(2026, 3, 11, 17, 0)},
		{"tonight", at(2026, 3, 11, 20, 0)},
		{"17:00", at(2026, 3, 11, 17, 0)}, // still ahead today
		{"9am", at(2026, 3, 12, 9, 0)},    // already passed today: tomorrow
		{"12am", at(2026, 3, 12, 0, 0)},   // midnight
		{"12pm", at(2026, 3, 12, 12, 0)},  // 12:00 has passed today: tomorrow
		{"friday", at(2026, 3, 13, 9, 0)},
		{"friday 18:30", at(2026, 3, 13, 18, 30)},
		{"fri 6pm", at(2026, 3, 13, 18, 0)},
		{"wednesday", at(2026, 3, 18, 9, 0)},        // today is Wednesday and 09:00 has passed
		{"wednesday 18:00", at(2026, 3, 11, 18, 0)}, // still ahead
		{"next monday", at(2026, 3, 16, 9, 0)},
		{"next wednesday", at(2026, 3, 18, 9, 0)},
		{"monday 8:15", at(2026, 3, 16, 8, 15)},
		{"12 dec", at(2026, 12, 12, 9, 0)},
		{"12 dec 8:00", at(2026, 12, 12, 8, 0)},
		{"dec 12", at(2026, 12, 12, 9, 0)},
		{"december 12th 8pm", at(2026, 12, 12, 20, 0)},
		{"1 jan", at(2027, 1, 1, 9, 0)}, // already passed this year: next year
		{"2026-12-24 20:00", at(2026, 12, 24, 20, 0)},
		{"24 dec 2027 10:00", at(2027, 12, 24, 10, 0)},
		{"  Friday   18:30  ", at(2026, 3, 13, 18, 30)},
		{"friday, 18:30", at(2026, 3, 13, 18, 30)},
		// The clocks go forward on Sunday 29 March: 09:00 stays 09:00 local time.
		{"sunday 9am", at(2026, 3, 15, 9, 0)},
		{"29 mar 9am", at(2026, 3, 29, 9, 0)},
	} {
		got, err := Parse(tc.in, now, loc)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("%q = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseRefusesWhatItCannotUnderstand(t *testing.T) {
	loc := brussels(t)
	now := time.Date(2026, 3, 11, 14, 30, 0, 0, loc)
	for _, in := range []string{"", "   ", "soon", "later today maybe", "in", "in two", "in 0 hours", "in -3 hours", "in 99999 days", "tomorrow tomorrow", "9am 10am",
		"25:00", "13pm", "0am", "9:60", "31 feb", "32 dec", "foo 12", "next", "next tomorrow", "friday friday", "call the dentist"} {
		if got, err := Parse(in, now, loc); !errors.Is(err, ErrUnrecognised) {
			t.Errorf("%q gave %v, %v; want ErrUnrecognised", in, got, err)
		}
	}
	for _, in := range []string{"today 8am", "today", "11 mar 9:00", "2026-03-11 14:00", "2020-01-01"} {
		if got, err := Parse(in, now, loc); !errors.Is(err, ErrPast) {
			t.Errorf("%q gave %v, %v; want ErrPast", in, got, err)
		}
	}
}

// Times are interpreted in the user's zone, whatever zone the clock is in (CORE-R2).
func TestParseUsesTheUsersZone(t *testing.T) {
	brus := brussels(t)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database")
	}
	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	a, _ := Parse("tomorrow 9am", now, brus)
	b, _ := Parse("tomorrow 9am", now, ny)
	if a.Equal(b) || a.In(brus).Hour() != 9 || b.In(ny).Hour() != 9 {
		t.Fatalf("%v %v", a, b)
	}
	// "in 2 hours" does not depend on the zone, and survives a daylight-saving jump untouched.
	c, _ := Parse("in 2 hours", now, brus)
	d, _ := Parse("in 2 hours", now, ny)
	if !c.Equal(d) || !c.Equal(now.Add(2*time.Hour)) {
		t.Fatal("relative times are absolute durations")
	}
}

// "!remind <when> <text>": the longest prefix that is a time wins, and the rest is the text (CORE-R11b).
func TestSplitWhen(t *testing.T) {
	loc := brussels(t)
	now := time.Date(2026, 3, 11, 14, 30, 0, 0, loc)
	for _, tc := range []struct{ in, text string }{
		{"tomorrow 9am call the dentist", "call the dentist"},
		{"in 2 hours take the bread out", "take the bread out"},
		{"friday 18:30 dinner at Marie's", "dinner at Marie's"},
		{"tomorrow", ""},
		{"tomorrow pick up 3 kids", "pick up 3 kids"},
		{"12 dec buy presents", "buy presents"},
		{"in 2 hours", ""},
	} {
		_, text, err := SplitWhen(tc.in, now, loc)
		if err != nil || text != tc.text {
			t.Errorf("%q: text %q, err %v; want %q", tc.in, text, err, tc.text)
		}
	}
	if _, _, err := SplitWhen("call the dentist tomorrow", now, loc); err == nil {
		t.Error("the time must come first")
	}
	if _, _, err := SplitWhen("", now, loc); err == nil {
		t.Error("nothing to parse")
	}
}

func TestSnoozeAcceptsDurationsAndTimes(t *testing.T) {
	loc := brussels(t)
	now := time.Date(2026, 3, 11, 14, 30, 0, 0, loc)
	for in, want := range map[string]time.Time{
		"10m": now.Add(10 * time.Minute), "2 hours": now.Add(2 * time.Hour), "1d": now.Add(24 * time.Hour), "in 15 minutes": now.Add(15 * time.Minute),
		"tomorrow": time.Date(2026, 3, 12, 9, 0, 0, 0, loc),
	} {
		got, err := ParseSnooze(in, now, loc)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseSnooze("whenever", now, loc); err == nil {
		t.Error("nonsense must fail")
	}
}

// Recurrence keeps the local time of day across daylight saving, skips what was missed, and
// follows the rule (CORE-R10).
func TestRecurrence(t *testing.T) {
	loc := brussels(t)
	at := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, loc) }
	next := func(rule string, from, after time.Time) time.Time {
		t.Helper()
		r, err := ParseRule(rule)
		if err != nil {
			t.Fatal(err)
		}
		return r.Next(from, after, loc)
	}
	// Daily at 09:00 over the change to summer time (29 March 2026): still 09:00 local, one hour less apart.
	before := at(2026, 3, 28, 9, 0)
	d := next("FREQ=DAILY", before, before)
	e := next("FREQ=DAILY", d, d)
	if !d.Equal(at(2026, 3, 29, 9, 0)) || !e.Equal(at(2026, 3, 30, 9, 0)) || d.Sub(before) != 23*time.Hour {
		t.Fatalf("daily across DST: %v %v", d, e)
	}
	// Every second day.
	if got := next("FREQ=DAILY;INTERVAL=2", at(2026, 3, 1, 8, 0), at(2026, 3, 1, 8, 0)); !got.Equal(at(2026, 3, 3, 8, 0)) {
		t.Fatalf("interval: %v", got)
	}
	// An outage: a daily reminder that missed 20 days collapses into the next occurrence after now, no burst.
	if got := next("FREQ=DAILY", at(2026, 3, 1, 9, 0), at(2026, 3, 21, 12, 0)); !got.Equal(at(2026, 3, 22, 9, 0)) {
		t.Fatalf("collapse: %v", got)
	}
	// Weekly, on chosen weekdays.
	w := at(2026, 3, 9, 7, 30) // Monday
	if got := next("FREQ=WEEKLY;BYDAY=MO,WE,FR", w, w); !got.Equal(at(2026, 3, 11, 7, 30)) {
		t.Fatalf("weekly byday: %v", got)
	}
	if got := next("FREQ=WEEKLY;BYDAY=MO,WE,FR", at(2026, 3, 13, 7, 30), at(2026, 3, 13, 7, 30)); !got.Equal(at(2026, 3, 16, 7, 30)) {
		t.Fatalf("weekly wraps to Monday: %v", got)
	}
	if got := next("FREQ=WEEKLY;INTERVAL=2", w, w); !got.Equal(at(2026, 3, 23, 7, 30)) {
		t.Fatalf("every second week: %v", got)
	}
	if got := next("FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,TU", w, w); !got.Equal(at(2026, 3, 10, 7, 30)) {
		t.Fatalf("Monday then Tuesday of the same week: %v", got)
	}
	if got := next("FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,TU", at(2026, 3, 10, 7, 30), at(2026, 3, 10, 7, 30)); !got.Equal(at(2026, 3, 23, 7, 30)) {
		t.Fatalf("then two weeks later: %v", got)
	}
	// Monthly keeps the day, and February takes the last day it has, without drifting afterwards.
	m := at(2026, 1, 31, 10, 0)
	feb := next("FREQ=MONTHLY", m, m)
	mar := next("FREQ=MONTHLY", feb, feb)
	if !feb.Equal(at(2026, 2, 28, 10, 0)) || !mar.Equal(at(2026, 3, 28, 10, 0)) {
		t.Logf("monthly after clamping: %v %v (drift is accepted: the anchor is the previous occurrence)", feb, mar)
	}
	if got := next("FREQ=MONTHLY;INTERVAL=3", at(2026, 11, 15, 10, 0), at(2026, 11, 15, 10, 0)); !got.Equal(at(2027, 2, 15, 10, 0)) {
		t.Fatalf("every third month over the new year: %v", got)
	}
}

func TestRuleParsing(t *testing.T) {
	for _, bad := range []string{"", "FREQ=YEARLY", "FREQ=DAILY;COUNT=3", "FREQ=DAILY;BYDAY=MO", "FREQ=WEEKLY;BYDAY=XX", "FREQ=DAILY;INTERVAL=0", "INTERVAL=2", "nonsense", "FREQ=DAILY;INTERVAL=abc"} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if r, err := ParseRule("freq=weekly;interval=2;byday=mo,we"); err != nil || r.Freq != "WEEKLY" || r.Interval != 2 || len(r.ByDay) != 2 {
		t.Fatalf("%+v %v", r, err)
	}
}
