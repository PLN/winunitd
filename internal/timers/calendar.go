package timers

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // IANA names (America/New_York, Europe/Berlin) on Windows
	"unicode"
)

// Calendar is a parsed OnCalendar= expression.
//
// MVP forms: "daily", "Mon..Fri 03:00", and "*-*-* HH:MM:SS"
// (seconds optional). Weekday names may be abbreviated or full.
type Calendar struct {
	Raw string

	// RestrictWeekdays is true when the expression names weekdays
	// (for example Mon..Fri). When false, every weekday matches.
	RestrictWeekdays bool
	Weekdays         [7]bool // indexed by time.Weekday

	// Year, Month, Day use -1 as wildcard (*).
	Year  int
	Month int
	Day   int

	Hour   int
	Minute int
	Second int
}

// AllowsWeekday reports whether d is included in the expression.
func (c Calendar) AllowsWeekday(d time.Weekday) bool {
	if !c.RestrictWeekdays {
		return true
	}
	return c.Weekdays[d]
}

// ParseCalendar parses an OnCalendar= value.
func ParseCalendar(s string) (Calendar, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Calendar{}, fmt.Errorf("empty calendar expression")
	}
	if strings.EqualFold(raw, "daily") {
		return Calendar{
			Raw:   raw,
			Year:  -1,
			Month: -1,
			Day:   -1,
		}, nil
	}

	fields := strings.Fields(raw)
	hour, minute, second, err := parseTimeOfDay(fields[len(fields)-1])
	if err != nil {
		return Calendar{}, fmt.Errorf("invalid calendar expression %q", raw)
	}
	rest := fields[:len(fields)-1]
	if len(rest) == 0 {
		return Calendar{}, fmt.Errorf("invalid calendar expression %q", raw)
	}

	cal := Calendar{
		Raw:    raw,
		Year:   -1,
		Month:  -1,
		Day:    -1,
		Hour:   hour,
		Minute: minute,
		Second: second,
	}
	sawDate := false
	sawWeekday := false
	for _, f := range rest {
		switch {
		case isDatePattern(f):
			if sawDate {
				return Calendar{}, fmt.Errorf("invalid calendar expression %q", raw)
			}
			y, m, d, err := parseDatePattern(f)
			if err != nil {
				return Calendar{}, fmt.Errorf("invalid calendar expression %q: %w", raw, err)
			}
			cal.Year, cal.Month, cal.Day = y, m, d
			sawDate = true
		case looksLikeWeekdays(f):
			days, err := parseWeekdays(f)
			if err != nil {
				return Calendar{}, fmt.Errorf("invalid calendar expression %q: %w", raw, err)
			}
			cal.RestrictWeekdays = true
			for _, wd := range days {
				cal.Weekdays[wd] = true
			}
			sawWeekday = true
		default:
			return Calendar{}, fmt.Errorf("invalid calendar expression %q", raw)
		}
	}
	if !sawDate && !sawWeekday {
		return Calendar{}, fmt.Errorf("invalid calendar expression %q", raw)
	}
	return cal, nil
}

func parseTimeOfDay(s string) (hour, minute, second int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("invalid time of day %q", s)
	}
	hour, err = parseBounded(parts[0], 0, 23)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid hour in %q", s)
	}
	minute, err = parseBounded(parts[1], 0, 59)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid minute in %q", s)
	}
	if len(parts) == 3 {
		second, err = parseBounded(parts[2], 0, 59)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid second in %q", s)
		}
	}
	return hour, minute, second, nil
}

func isDatePattern(s string) bool {
	parts := strings.Split(s, "-")
	return len(parts) == 3
}

func parseDatePattern(s string) (year, month, day int, err error) {
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("invalid date pattern %q", s)
	}
	year, err = parseWildcardInt(parts[0], 1, 9999)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid year in %q", s)
	}
	month, err = parseWildcardInt(parts[1], 1, 12)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid month in %q", s)
	}
	day, err = parseWildcardInt(parts[2], 1, 31)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid day in %q", s)
	}
	return year, month, day, nil
}

func parseWildcardInt(s string, min, max int) (int, error) {
	if s == "*" {
		return -1, nil
	}
	return parseBounded(s, min, max)
}

func parseBounded(s string, min, max int) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return 0, fmt.Errorf("invalid number %q", s)
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if n < min || n > max {
		return 0, fmt.Errorf("value %d out of range %d..%d", n, min, max)
	}
	return n, nil
}

func looksLikeWeekdays(s string) bool {
	tok := s
	if i := strings.IndexAny(s, ".,"); i >= 0 {
		tok = s[:i]
	}
	_, ok := weekdayName(tok)
	return ok
}

func parseWeekdays(s string) ([]time.Weekday, error) {
	var days []time.Weekday
	seen := [7]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty weekday list")
		}
		if from, to, ok := strings.Cut(part, ".."); ok {
			a, ok := weekdayName(from)
			if !ok {
				return nil, fmt.Errorf("invalid weekday %q", from)
			}
			b, ok := weekdayName(to)
			if !ok {
				return nil, fmt.Errorf("invalid weekday %q", to)
			}
			d := a
			for {
				if !seen[d] {
					days = append(days, d)
					seen[d] = true
				}
				if d == b {
					break
				}
				d = (d + 1) % 7
			}
			continue
		}
		wd, ok := weekdayName(part)
		if !ok {
			return nil, fmt.Errorf("invalid weekday %q", part)
		}
		if !seen[wd] {
			days = append(days, wd)
			seen[wd] = true
		}
	}
	if len(days) == 0 {
		return nil, fmt.Errorf("empty weekday list")
	}
	return days, nil
}

// Next returns the next matching instant strictly after after.
// Zero means no future match in a reasonable search window.
func (c Calendar) Next(after time.Time) time.Time {
	return c.search(after, false)
}

// Previous returns the latest matching instant strictly before before.
// Zero means no past match in a reasonable search window.
func (c Calendar) Previous(before time.Time) time.Time {
	return c.search(before, true)
}

func (c Calendar) search(from time.Time, backward bool) time.Time {
	loc := from.Location()
	if loc == nil {
		loc = time.Local
	}
	if c.Year >= 0 && c.Month >= 0 && c.Day >= 0 {
		cand := c.wallTimeOn(c.Year, time.Month(c.Month), c.Day, loc)
		if cand.IsZero() || !c.AllowsWeekday(cand.Weekday()) {
			return time.Time{}
		}
		if backward && cand.Before(from) {
			return cand
		}
		if !backward && cand.After(from) {
			return cand
		}
		return time.Time{}
	}

	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	if backward {
		if latest := c.boundDay(loc, true); !latest.IsZero() && latest.Before(start) {
			start = latest
		}
		if c.Year >= 0 && start.Year() < c.Year {
			return time.Time{}
		}
	} else {
		if earliest := c.boundDay(loc, false); !earliest.IsZero() && earliest.After(start) {
			start = earliest
		}
		if c.Year >= 0 && start.Year() > c.Year {
			return time.Time{}
		}
	}

	const maxDays = 366*8 + 2
	for i := 0; i < maxDays; i++ {
		var day time.Time
		if backward {
			day = start.AddDate(0, 0, -i)
		} else {
			day = start.AddDate(0, 0, i)
		}
		if c.Year >= 0 {
			if !backward && day.Year() > c.Year {
				return time.Time{}
			}
			if backward && day.Year() < c.Year {
				return time.Time{}
			}
		}
		if !c.dateMatches(day) {
			continue
		}
		cand := c.wallTimeOn(day.Year(), day.Month(), day.Day(), loc)
		if cand.IsZero() || !c.AllowsWeekday(cand.Weekday()) {
			continue
		}
		if backward {
			if cand.Before(from) {
				return cand
			}
			continue
		}
		if cand.After(from) {
			return cand
		}
	}
	return time.Time{}
}

// dateMatches reports whether day's civil Y/M/D satisfy the expression.
// Wildcards (-1) match any value. The civil date is taken as-is; callers
// must not substitute fixed fields into an unrelated day.
func (c Calendar) dateMatches(day time.Time) bool {
	if c.Year >= 0 && day.Year() != c.Year {
		return false
	}
	if c.Month >= 0 && int(day.Month()) != c.Month {
		return false
	}
	if c.Day >= 0 && day.Day() != c.Day {
		return false
	}
	return true
}

// boundDay is the earliest (end=false) or latest (end=true) midnight whose
// civil date could match the fixed fields. Zero means no useful bound
// (every field is a wildcard, or the constructed date is not itself a match,
// for example 2027-02-31).
func (c Calendar) boundDay(loc *time.Location, end bool) time.Time {
	if c.Year < 0 && c.Month < 0 && c.Day < 0 {
		return time.Time{}
	}
	y, m, d := 1, 1, 1
	if end {
		y, m, d = 9999, 12, 31
	}
	if c.Year >= 0 {
		y = c.Year
	}
	if c.Month >= 0 {
		m = c.Month
	}
	if c.Day >= 0 {
		d = c.Day
	}
	day := time.Date(y, time.Month(m), d, 0, 0, 0, 0, loc)
	if !c.dateMatches(day) {
		return time.Time{}
	}
	return day
}

// wallTimeOn maps the expression's wall clock onto a civil date.
//
// A candidate whose year/month/day after construction is not the intended
// civil date is rejected (time.Date overflow: Feb 31 → Mar 3).
// If the wall time does not exist (DST spring-forward gap), the result is
// the first valid instant after the gap. If it exists twice (fall-back),
// the result is the first occurrence (DESIGN.md §18).
func (c Calendar) wallTimeOn(year int, month time.Month, day int, loc *time.Location) time.Time {
	cand := time.Date(year, month, day, c.Hour, c.Minute, c.Second, 0, loc)
	if cand.Year() != year || cand.Month() != month || cand.Day() != day {
		return time.Time{}
	}
	if c.wallOK(cand) {
		return firstOccurrence(cand)
	}
	return firstValidAfterGap(year, month, day, c.Hour, c.Minute, c.Second, loc)
}

func (c Calendar) wallOK(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	return t.Hour() == c.Hour && t.Minute() == c.Minute && t.Second() == c.Second
}

// firstOccurrence returns the earliest instant with the same civil date and
// wall clock as t. During a DST fall-back fold, time.Date may pick either
// copy depending on the zone; this pins the first (systemd: first occurrence).
func firstOccurrence(t time.Time) time.Time {
	y, m, d := t.Date()
	h, min, s := t.Clock()
	for {
		earlier := t.Add(-time.Hour)
		if earlier.Year() != y || earlier.Month() != m || earlier.Day() != d {
			return t
		}
		if earlier.Hour() != h || earlier.Minute() != min || earlier.Second() != s {
			return t
		}
		t = earlier
	}
}

// firstValidAfterGap returns the first existing wall-clock instant at or
// after hour:min:sec on the given civil date. Used when the requested wall
// time falls in a DST spring-forward gap (time.Date does not round-trip;
// in America/New_York it can land before the gap, in Europe/Berlin after).
func firstValidAfterGap(year int, month time.Month, day, hour, min, sec int, loc *time.Location) time.Time {
	start := hour*3600 + min*60 + sec
	for sod := start; sod < 24*3600; sod++ {
		h := sod / 3600
		mi := (sod / 60) % 60
		s := sod % 60
		t := time.Date(year, month, day, h, mi, s, 0, loc)
		if t.Year() == year && t.Month() == month && t.Day() == day &&
			t.Hour() == h && t.Minute() == mi && t.Second() == s {
			return firstOccurrence(t)
		}
	}
	return time.Time{}
}

func weekdayName(s string) (time.Weekday, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "sun", "sunday":
		return time.Sunday, true
	case "mon", "monday":
		return time.Monday, true
	case "tue", "tues", "tuesday":
		return time.Tuesday, true
	case "wed", "wednesday":
		return time.Wednesday, true
	case "thu", "thur", "thurs", "thursday":
		return time.Thursday, true
	case "fri", "friday":
		return time.Friday, true
	case "sat", "saturday":
		return time.Saturday, true
	default:
		return 0, false
	}
}
