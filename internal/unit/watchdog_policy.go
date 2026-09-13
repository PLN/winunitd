package unit

import (
	"strconv"
)

func (p *parser) finishWatchdogPolicy(spec *ServiceSpec, s *serviceBuilder) {
	if s.watchdogGraceL == 0 && s.watchdogTimeoutL == 0 && s.watchdogThresholdL == 0 {
		return
	}
	if p.unit.FormatVersion != 2 || !spec.WatchdogProbeMode() || spec.IsExternalProxy() {
		p.errorf(0, "WatchdogGraceSec, WatchdogTimeoutSec and WatchdogFailureThreshold require FormatVersion=2 and a managed tcp/http watchdog")
		return
	}
	if s.watchdogGraceL != 0 {
		d, err := ParseDuration(s.watchdogGrace)
		if err != nil || d == maxDuration {
			p.errorf(s.watchdogGraceL, "WatchdogGraceSec must be a finite nonnegative duration")
		} else {
			spec.WatchdogGraceSec = d
		}
	}
	if s.watchdogTimeoutL != 0 {
		d, err := ParseDuration(s.watchdogTimeout)
		if err != nil || d <= 0 || d == maxDuration {
			p.errorf(s.watchdogTimeoutL, "WatchdogTimeoutSec must be a positive finite duration")
		} else {
			spec.WatchdogTimeoutSec = d
		}
	}
	if s.watchdogThresholdL != 0 {
		n, err := strconv.Atoi(s.watchdogThreshold)
		if err != nil || n < 1 || n > 1000 {
			p.errorf(s.watchdogThresholdL, "WatchdogFailureThreshold must be an integer from 1 to 1000")
		} else {
			spec.WatchdogFailureThreshold = n
		}
	}
}
