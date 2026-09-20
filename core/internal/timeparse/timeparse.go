// Package timeparse understands the time expressions people type in chat ("tomorrow 9am", "in 2
// hours", "friday 18:30") and the recurrence rules of reminders (docs/design/04-ingestion.md
// section 3.1, requirements CORE-R2, CORE-R10, CORE-R11, CORE-R13). It is English only in v1, has
// no dependencies and is deterministic: the same input, clock and zone always give the same instant.
package timeparse

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// Errors.
var (
	ErrUnrecognised = errors.New("could not understand the time")
	ErrPast         = errors.New("that time has already passed")
)

// DefaultHour is the time of day used when only a day is given.
const DefaultHour = 9

// Example is shown when an expression cannot be understood.
const Example = `for example "in 2 hours", "tomorrow 9am", "friday 18:30" or "12 dec 8:00"`

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday, "mon": time.Monday, "monday": time.Monday, "tue": time.Tuesday, "tues": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "weds": time.Wednesday, "wednesday": time.Wednesday, "thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday, "sat": time.Saturday, "saturday": time.Saturday,
}

var months = map[string]time.Month{
	"jan": time.January, "january": time.January, "feb": time.February, "february": time.February, "mar": time.March, "march": time.March,
	"apr": time.April, "april": time.April, "may": time.May, "jun": time.June, "june": time.June, "jul": time.July, "july": time.July,
	"aug": time.August, "august": time.August, "sep": time.September, "sept": time.September, "september": time.September,
	"oct": time.October, "october": time.October, "nov": time.November, "november": time.November, "dec": time.December, "december": time.December,
}

var units = map[string]time.Duration{
	"m": time.Minute, "min": time.Minute, "mins": time.Minute, "minute": time.Minute, "minutes": time.Minute,
	"h": time.Hour, "hr": time.Hour, "hrs": time.Hour, "hour": time.Hour, "hours": time.Hour,
	"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
	"w": 7 * 24 * time.Hour, "week": 7 * 24 * time.Hour, "weeks": 7 * 24 * time.Hour,
}

func fields(s string) []string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(",", " ").Replace(s)
	return strings.Fields(s)
}

// Parse turns an expression into an instant after now, in the zone loc. Bare times mean the next
// such time; a day alone means DefaultHour that day; a weekday alone is the next such day.
func Parse(input string, now time.Time, loc *time.Location) (time.Time, error) {
	tok := fields(input)
	if len(tok) == 0 {
		return time.Time{}, ErrUnrecognised
	}
	local := now.In(loc)
	if t, ok, err := parseRelative(tok, now); ok {
		return t, err
	}
	var (
		date       *time.Time // the day, at midnight local
		hour, min  = -1, 0
		weekday    = false
		nextPrefix = false
	)
	setDay := func(d time.Time) bool {
		if date != nil {
			return false
		}
		date = &d
		return true
	}
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < len(tok); i++ {
		w := tok[i]
		switch {
		case w == "at" || w == "on" || w == "@":
		case w == "today":
			if !setDay(today) {
				return time.Time{}, ErrUnrecognised
			}
		case w == "tomorrow" || w == "tmrw" || w == "tmr":
			if !setDay(today.AddDate(0, 0, 1)) {
				return time.Time{}, ErrUnrecognised
			}
		case w == "tonight":
			if !setDay(today) || hour >= 0 {
				return time.Time{}, ErrUnrecognised
			}
			hour = 20
		case w == "next" && i+1 < len(tok) && weekdayOf(tok[i+1]) >= 0:
			nextPrefix = true
		case weekdayOf(w) >= 0:
			wd := time.Weekday(weekdayOf(w))
			d := today
			delta := (int(wd) - int(today.Weekday()) + 7) % 7
			if nextPrefix && delta == 0 {
				delta = 7
			}
			if !setDay(d.AddDate(0, 0, delta)) {
				return time.Time{}, ErrUnrecognised
			}
			weekday, nextPrefix = true, false
		case w == "noon":
			if hour >= 0 {
				return time.Time{}, ErrUnrecognised
			}
			hour, min = 12, 0
		case w == "midnight":
			if hour >= 0 {
				return time.Time{}, ErrUnrecognised
			}
			hour, min = 0, 0
		default:
			if h, m, ok, consumed := parseClock(tok, i); ok {
				if hour >= 0 {
					return time.Time{}, ErrUnrecognised
				}
				hour, min = h, m
				i += consumed - 1
				continue
			}
			if d, y, consumed, ok := parseDate(tok, i, today); ok {
				if !setDay(d) {
					return time.Time{}, ErrUnrecognised
				}
				_ = y
				i += consumed - 1
				continue
			}
			return time.Time{}, ErrUnrecognised
		}
	}
	if date == nil && hour < 0 {
		return time.Time{}, ErrUnrecognised
	}
	if nextPrefix {
		return time.Time{}, ErrUnrecognised
	}
	if hour < 0 {
		hour = DefaultHour
	}
	if date == nil { // a time alone: the next time it is that time
		d := time.Date(today.Year(), today.Month(), today.Day(), hour, min, 0, 0, loc)
		if !d.After(now) {
			d = time.Date(today.Year(), today.Month(), today.Day()+1, hour, min, 0, 0, loc)
		}
		return d, nil
	}
	at := func(d time.Time) time.Time { return time.Date(d.Year(), d.Month(), d.Day(), hour, min, 0, 0, loc) }
	t := at(*date)
	if !t.After(now) {
		if !weekday {
			return time.Time{}, ErrPast
		}
		t = at(date.AddDate(0, 0, 7)) // "friday" said on a friday after that time: next week
	}
	return t, nil
}

func weekdayOf(w string) int {
	if d, ok := weekdays[w]; ok {
		return int(d)
	}
	return -1
}

// parseRelative handles "in 2 hours", "in a day", "in half an hour".
func parseRelative(tok []string, now time.Time) (time.Time, bool, error) {
	if tok[0] != "in" {
		return time.Time{}, false, nil
	}
	rest := tok[1:]
	if len(rest) == 3 && rest[0] == "half" && rest[1] == "an" && (rest[2] == "hour" || rest[2] == "hr") {
		return now.Add(30 * time.Minute), true, nil
	}
	if len(rest) == 0 {
		return time.Time{}, true, ErrUnrecognised
	}
	var n float64
	var unit string
	switch {
	case len(rest) == 2 && (rest[0] == "a" || rest[0] == "an"):
		n, unit = 1, rest[1]
	case len(rest) == 2:
		f, err := strconv.ParseFloat(rest[0], 64)
		if err != nil {
			return time.Time{}, true, ErrUnrecognised
		}
		n, unit = f, rest[1]
	case len(rest) == 1: // "in 2h", "in 45m"
		s := rest[0]
		i := 0
		for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
			i++
		}
		f, err := strconv.ParseFloat(s[:i], 64)
		if err != nil || i == len(s) {
			return time.Time{}, true, ErrUnrecognised
		}
		n, unit = f, s[i:]
	default:
		return time.Time{}, true, ErrUnrecognised
	}
	d, ok := units[unit]
	if !ok || n <= 0 || n > 10000 {
		return time.Time{}, true, ErrUnrecognised
	}
	return now.Add(time.Duration(n * float64(d))), true, nil
}

// parseClock reads "9", "9am", "9:30", "9:30pm", "18:30", "9 am" starting at tok[i].
func parseClock(tok []string, i int) (h, m int, ok bool, consumed int) {
	s := tok[i]
	meridiem := ""
	consumed = 1
	for _, suf := range []string{"am", "pm"} {
		if strings.HasSuffix(s, suf) {
			meridiem, s = suf, strings.TrimSuffix(s, suf)
		}
	}
	if meridiem == "" && i+1 < len(tok) && (tok[i+1] == "am" || tok[i+1] == "pm") {
		meridiem = tok[i+1]
		consumed = 2
	}
	hs, ms, hasColon := strings.Cut(s, ":")
	hv, err := strconv.Atoi(hs)
	if err != nil || hs == "" || len(hs) > 2 {
		return 0, 0, false, 0
	}
	mv := 0
	if hasColon {
		if len(ms) != 2 {
			return 0, 0, false, 0
		}
		if mv, err = strconv.Atoi(ms); err != nil {
			return 0, 0, false, 0
		}
	} else if meridiem == "" {
		return 0, 0, false, 0 // a bare number is not a time ("in 2" or a day of the month)
	}
	if mv < 0 || mv > 59 {
		return 0, 0, false, 0
	}
	switch meridiem {
	case "am", "pm":
		if hv < 1 || hv > 12 {
			return 0, 0, false, 0
		}
		if hv == 12 {
			hv = 0
		}
		if meridiem == "pm" {
			hv += 12
		}
	default:
		if hv > 23 {
			return 0, 0, false, 0
		}
	}
	return hv, mv, true, consumed
}

// parseDate reads "2026-12-12", "12 dec", "dec 12", "12 december 2027" starting at tok[i].
func parseDate(tok []string, i int, today time.Time) (d time.Time, hasYear bool, consumed int, ok bool) {
	loc := today.Location()
	if t, err := time.ParseInLocation("2006-01-02", tok[i], loc); err == nil {
		return t, true, 1, true
	}
	build := func(day int, mon time.Month, rest []string) (time.Time, bool, int, bool) {
		year, used := today.Year(), 0
		if len(rest) > 0 {
			if y, err := strconv.Atoi(rest[0]); err == nil && y >= 2000 && y <= 2200 {
				year, used = y, 1
			}
		}
		t := time.Date(year, mon, day, 0, 0, 0, 0, loc)
		if t.Day() != day { // 31 feb
			return time.Time{}, false, 0, false
		}
		if used == 0 && t.Before(today) {
			t = t.AddDate(1, 0, 0)
		}
		return t, used == 1, used, true
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(tok[i], "st"), "nd"), "rd"), "th")); err == nil && i+1 < len(tok) {
		if mon, isMonth := months[tok[i+1]]; isMonth && n >= 1 && n <= 31 {
			t, y, used, ok := build(n, mon, tok[i+2:])
			return t, y, 2 + used, ok
		}
	}
	if mon, isMonth := months[tok[i]]; isMonth && i+1 < len(tok) {
		if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(tok[i+1], "st"), "nd"), "rd"), "th")); err == nil && n >= 1 && n <= 31 {
			t, y, used, ok := build(n, mon, tok[i+2:])
			return t, y, 2 + used, ok
		}
	}
	return time.Time{}, false, 0, false
}

// SplitWhen separates a time expression from the text that follows it: "tomorrow 9am call the
// dentist" is ("tomorrow 9am", "call the dentist"). The longest prefix that parses wins.
func SplitWhen(args string, now time.Time, loc *time.Location) (when time.Time, text string, err error) {
	words := strings.Fields(args)
	lastErr := ErrUnrecognised
	for n := min(len(words), 6); n >= 1; n-- {
		t, e := Parse(strings.Join(words[:n], " "), now, loc)
		if e == nil {
			return t, strings.TrimSpace(strings.Join(words[n:], " ")), nil
		}
		if errors.Is(e, ErrPast) && n == min(len(words), 6) {
			lastErr = e
		}
	}
	return time.Time{}, "", lastErr
}

// ParseSnooze accepts a duration ("10m", "2 hours", "1d") or any time expression, and returns the new due time.
func ParseSnooze(input string, now time.Time, loc *time.Location) (time.Time, error) {
	tok := fields(input)
	if len(tok) > 0 && tok[0] != "in" {
		if t, ok, err := parseRelative(append([]string{"in"}, tok...), now); ok && err == nil {
			return t, nil
		}
	}
	return Parse(input, now, loc)
}

// Describe formats an instant for a reply, in the zone.
func Describe(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("Mon 2 Jan, 15:04")
}
