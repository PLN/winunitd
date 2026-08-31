package timers

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
