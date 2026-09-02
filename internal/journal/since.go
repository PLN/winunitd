package journal

import (
	"fmt"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/unit"
)

// ParseSince parses LogsParams.Since / winctl --since.
// Accepted forms: RFC3339 (nano or second), a date YYYY-MM-DD (UTC midnight),
// a unit-file duration subtracted from now ("1h", "1d", "30min", "1h 30min"),
// and the same duration with a trailing " ago" ("1 hour ago", "1 day 2 hours ago").
// Empty is an error — callers that mean "no filter" must not call this.
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
	rel := low
	if before, ok := strings.CutSuffix(low, " ago"); ok {
		rel = strings.TrimSpace(before)
	}
	if d, err := unit.ParseDuration(rel); err == nil {
		if d < 0 {
			d = -d
		}
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("cannot parse since %q", s)
}
