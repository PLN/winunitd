package core

import "time"

// ReasonStartLimit is the winctl status reason when StartLimitBurst
// starts fall inside StartLimitIntervalSec (DESIGN.md §20, §44).
const ReasonStartLimit = "start-limit"

// StartLimitDisabled reports an explicit opt-out: Burst 0 or a non-positive
// interval means the manager never hits the start limit.
func StartLimitDisabled(interval time.Duration, burst int) bool {
	return burst <= 0 || interval <= 0
}

// StartLimitHit reports that burst starts already fall inside interval
// ending at now, so another Restart= relaunch must not be scheduled.
func StartLimitHit(starts []time.Time, now time.Time, interval time.Duration, burst int) bool {
	if StartLimitDisabled(interval, burst) {
		return false
	}
	return countStartsInWindow(starts, now, interval) >= burst
}

// RecordStart appends now and drops timestamps older than the window.
// Burst 0 / non-positive interval does not retain timestamps.
func RecordStart(starts []time.Time, now time.Time, interval time.Duration, burst int) []time.Time {
	if StartLimitDisabled(interval, burst) {
		return nil
	}
	cutoff := now.Add(-interval)
	n := 0
	for _, t := range starts {
		if !t.Before(cutoff) {
			starts[n] = t
			n++
		}
	}
	return append(starts[:n], now)
}

func countStartsInWindow(starts []time.Time, now time.Time, interval time.Duration) int {
	cutoff := now.Add(-interval)
	n := 0
	for _, t := range starts {
		if !t.Before(cutoff) {
			n++
		}
	}
	return n
}
