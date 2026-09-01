package journal

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseSince parses LogsParams.Since / winctl --since.
// Accepted forms: RFC3339 (nano or second), a date YYYY-MM-DD (UTC midnight),
// a Go duration subtracted from now ("1h", "30m"), and "N <unit> ago"
// (seconds/minutes/hours/days/weeks). Empty is an error — callers that
// mean "no filter" must not call this.
func ParseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("since value required")
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.UTC); err == nil {
		return t, nil
	}
	low := strings.ToLower(s)
	if before, ok := strings.CutSuffix(low, " ago"); ok {
		d, err := parseAgo(strings.TrimSpace(before))
		if err != nil {
			return time.Time{}, fmt.Errorf("cannot parse since %q", s)
		}
		return now.Add(-d), nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			d = -d
		}
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("cannot parse since %q", s)
}

func parseAgo(rel string) (time.Duration, error) {
	parts := strings.Fields(rel)
	if len(parts) != 2 {
		return 0, fmt.Errorf("bad relative time")
	}
	n, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || n < 0 || n != n {
		return 0, fmt.Errorf("bad relative time")
	}
	unit := strings.TrimSuffix(parts[1], "s")
	var d time.Duration
	switch unit {
	case "second":
		d = time.Duration(n * float64(time.Second))
	case "minute":
		d = time.Duration(n * float64(time.Minute))
	case "hour":
		d = time.Duration(n * float64(time.Hour))
	case "day":
		d = time.Duration(n * float64(24*time.Hour))
	case "week":
		d = time.Duration(n * float64(7*24*time.Hour))
	default:
		return 0, fmt.Errorf("bad relative unit")
	}
	if d < 0 {
		return 0, fmt.Errorf("bad relative time")
	}
	return d, nil
}
