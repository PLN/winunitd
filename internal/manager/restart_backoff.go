package manager

import "time"

// The accepted attempt is a saturating exponent, not an unbounded history.
// Check before multiplication so even the largest duration cannot overflow.
func cappedRestartDelay(base, cap time.Duration, attempt uint32) time.Duration {
	if base <= 0 || cap <= 0 {
		return base
	}
	for attempt > 0 && base < cap {
		if base > cap/2 {
			return cap
		}
		base *= 2
		attempt--
	}
	return min(base, cap)
}
