package unit

import (
	"strings"
	"time"
)

func (p *parser) finishRestartBackoff(spec *ServiceSpec, s *serviceBuilder) {
	if p.unit.FormatVersion != 2 {
		if s.restartBackoffL != 0 {
			p.errorf(s.restartBackoffL, "RestartBackoff requires FormatVersion=2")
		}
		if s.restartMaxDelayL != 0 {
			p.errorf(s.restartMaxDelayL, "RestartMaxDelaySec requires FormatVersion=2")
		}
		return
	}
	if s.restartBackoffL == 0 && s.restartMaxDelayL == 0 {
		return // retain the existing fixed-delay default
	}
	spec.RestartBackoff = strings.ToLower(strings.TrimSpace(s.restartBackoff))
	if spec.RestartBackoff != "fixed" && spec.RestartBackoff != "exponential" {
		p.errorf(s.restartBackoffL, "RestartBackoff must be fixed or exponential")
		return
	}
	if spec.RestartBackoff == "fixed" {
		if s.restartMaxDelayL != 0 {
			p.errorf(s.restartMaxDelayL, "RestartMaxDelaySec requires RestartBackoff=exponential")
		}
		return
	}
	cap, err := ParseDuration(s.restartMaxDelay)
	if err != nil || cap <= 0 || cap == maxDuration {
		p.errorf(s.restartMaxDelayL, "RestartMaxDelaySec must be a positive finite duration for exponential backoff")
		return
	}
	base := 100 * time.Millisecond
	if spec.RestartSecSet {
		base = spec.RestartSec
	}
	if base <= 0 || base > cap {
		p.errorf(s.restartSecL, "exponential RestartSec must be positive and no greater than RestartMaxDelaySec")
		return
	}
	spec.RestartMaxDelaySec = cap
}
