package unit

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (s *ServiceSpec) HasReadinessProbe() bool {
	return s != nil && (s.ReadinessMode == WatchdogModeTCP || s.ReadinessMode == WatchdogModeHTTP)
}

func (s *ServiceSpec) WaitsForReadiness() bool {
	return s != nil && (s.Type == TypeNotify || s.HasReadinessProbe())
}

func (s *ServiceSpec) ProbeReadiness(ctx context.Context) error {
	switch s.ReadinessMode {
	case WatchdogModeTCP:
		return ProbeTCP(ctx, s.ReadinessAddr)
	case WatchdogModeHTTP:
		return ProbeHTTP(ctx, s.ReadinessURL, s.ReadinessExpectedStatus)
	default:
		return fmt.Errorf("readiness probe is not configured")
	}
}

func (p *parser) finishReadiness(spec *ServiceSpec, s *serviceBuilder) {
	if s.readinessModeL == 0 && s.readinessEndpointL == 0 && s.readinessStatusL == 0 && s.readinessIntervalL == 0 && s.readinessTimeoutL == 0 {
		return
	}
	if p.unit.FormatVersion != 2 || spec.Type != TypeSimple {
		p.errorf(0, "readiness probes require FormatVersion=2 and Type=simple")
		return
	}
	spec.ReadinessMode = WatchdogMode(strings.ToLower(strings.TrimSpace(s.readinessMode)))
	spec.ReadinessEndpoint = strings.TrimSpace(s.readinessEndpoint)
	switch spec.ReadinessMode {
	case WatchdogModeTCP:
		addr, err := parseTCPWatchdogEndpoint(spec.ReadinessEndpoint)
		if err != nil {
			p.errorf(s.readinessEndpointL, "invalid ReadinessEndpoint: %v", err)
		} else {
			spec.ReadinessAddr = addr
		}
	case WatchdogModeHTTP:
		u, err := parseHTTPWatchdogEndpoint(spec.ReadinessEndpoint)
		if err != nil {
			p.errorf(s.readinessEndpointL, "invalid ReadinessEndpoint: %v", err)
		} else {
			spec.ReadinessURL = u.String()
		}
		spec.ReadinessExpectedStatus = defaultHTTPWatchdogStatus
	default:
		p.errorf(s.readinessModeL, "ReadinessMode must be tcp or http")
	}
	if s.readinessStatusL != 0 {
		status, err := parseHTTPWatchdogStatus(s.readinessStatus)
		if spec.ReadinessMode != WatchdogModeHTTP || err != nil {
			p.errorf(s.readinessStatusL, "ReadinessExpectedStatus requires HTTP mode and an integer from 100 to 599")
		} else {
			spec.ReadinessExpectedStatus = status
		}
	}
	spec.ReadinessIntervalSec = 100 * time.Millisecond
	spec.ReadinessTimeoutSec = time.Second
	for _, field := range []struct {
		name, raw string
		line      int
		out       *time.Duration
	}{
		{"ReadinessIntervalSec", s.readinessInterval, s.readinessIntervalL, &spec.ReadinessIntervalSec},
		{"ReadinessTimeoutSec", s.readinessTimeout, s.readinessTimeoutL, &spec.ReadinessTimeoutSec},
	} {
		if field.line == 0 {
			continue
		}
		d, err := ParseDuration(field.raw)
		if err != nil || d <= 0 || d == maxDuration {
			p.errorf(field.line, "%s must be a positive finite duration", field.name)
		} else {
			*field.out = d
		}
	}
	if !spec.TimeoutStartSecSet {
		spec.TimeoutStartSec = 90 * time.Second
		spec.TimeoutStartSecSet = true
	}
	if spec.TimeoutStartSec <= 0 || spec.TimeoutStartSec == maxDuration {
		p.errorf(s.timeoutStartL, "startup probes require a positive finite TimeoutStartSec")
	}
}
