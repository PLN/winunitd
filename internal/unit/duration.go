package unit

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxDuration = time.Duration(math.MaxInt64)

// ParseDuration parses a systemd-style time span.
// A bare number is seconds. Units may be concatenated or whitespace-separated
// (for example "5s", "1h 30min"). "infinity" is the maximum duration.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.EqualFold(s, "infinity") {
		return maxDuration, nil
	}

	var total time.Duration
	i := 0
	n := len(s)
	parsed := false
	for i < n {
		for i < n && unicode.IsSpace(rune(s[i])) {
			i++
		}
		if i >= n {
			break
		}
		start := i
		for i < n && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
			i++
		}
		if i == start {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		numStr := s[start:i]
		for i < n && unicode.IsSpace(rune(s[i])) {
			i++
		}
		uStart := i
		for i < n {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == 'µ' || r == 'μ' {
				i += size
				continue
			}
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				i += size
				continue
			}
			break
		}
		unit := s[uStart:i]
		if unit == "" {
			unit = "s"
		}
		nval, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		d, err := durationUnit(unit, nval)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
		if d > 0 && total > maxDuration-d {
			return 0, fmt.Errorf("invalid duration %q: duration overflow", s)
		}
		total += d
		parsed = true
	}
	if !parsed {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return total, nil
}

func durationUnit(unit string, n float64) (time.Duration, error) {
	mult, ok := durationUnits[strings.ToLower(unit)]
	if !ok {
		return 0, fmt.Errorf("unknown unit %q", unit)
	}
	if n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, fmt.Errorf("duration overflow")
	}
	maxN := float64(maxDuration) / float64(mult)
	if n > maxN {
		return 0, fmt.Errorf("duration overflow")
	}
	ns := n * float64(mult)
	if ns > float64(maxDuration) {
		return 0, fmt.Errorf("duration overflow")
	}
	d := time.Duration(ns)
	if d < 0 {
		return 0, fmt.Errorf("duration overflow")
	}
	return d, nil
}

var durationUnits = map[string]time.Duration{
	"us":      time.Microsecond,
	"usec":    time.Microsecond,
	"µs":      time.Microsecond,
	"μs":      time.Microsecond,
	"ms":      time.Millisecond,
	"msec":    time.Millisecond,
	"s":       time.Second,
	"sec":     time.Second,
	"second":  time.Second,
	"seconds": time.Second,
	"m":       time.Minute,
	"min":     time.Minute,
	"minute":  time.Minute,
	"minutes": time.Minute,
	"h":       time.Hour,
	"hr":      time.Hour,
	"hour":    time.Hour,
	"hours":   time.Hour,
	"d":       24 * time.Hour,
	"day":     24 * time.Hour,
	"days":    24 * time.Hour,
	"w":       7 * 24 * time.Hour,
	"week":    7 * 24 * time.Hour,
	"weeks":   7 * 24 * time.Hour,
}
