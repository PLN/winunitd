package unit

import (
	"fmt"
	"time"

	"github.com/PLN/winunitd/internal/timers"
)

// Kind is the unit type implied by the file suffix.
type Kind string

const (
	KindService Kind = "service"
	KindTimer   Kind = "timer"
	KindTarget  Kind = "target"
)

// ServiceType is a [Service] Type= value.
type ServiceType string

const (
	TypeSimple  ServiceType = "simple"
	TypeOneshot ServiceType = "oneshot"
	TypeNotify  ServiceType = "notify"
)

// RestartPolicy is a [Service] Restart= value.
type RestartPolicy string

const (
	RestartNo         RestartPolicy = "no"
	RestartAlways     RestartPolicy = "always"
	RestartOnFailure  RestartPolicy = "on-failure"
	RestartOnWatchdog RestartPolicy = "on-watchdog"
)

// NotifyAccess is a [Service] NotifyAccess= value. P4 supports main only.
type NotifyAccess string

const NotifyAccessMain NotifyAccess = "main"

// WatchdogMode is a [Service] WatchdogMode= value (DESIGN.md §19).
type WatchdogMode string

const (
	WatchdogModeNotify WatchdogMode = "notify"
	WatchdogModeTCP    WatchdogMode = "tcp"
	WatchdogModeHTTP   WatchdogMode = "http"
)

// EnvVar is a single Environment= assignment. Values are stored literally;
// ${} and %VAR% are not expanded.
type EnvVar struct {
	Name  string
	Value string
}

// Unit is a parsed unit file.
type Unit struct {
	Name string
	Path string
	Kind Kind

	Description string
	Requires    []string
	Wants       []string
	After       []string
	Before      []string

	// RequiresInteractiveSession skips the unit when no suitable
	// interactive session exists (DESIGN.md §15). SessionMode is not
	// implemented.
	RequiresInteractiveSession bool

	Service *ServiceSpec
	Timer   *TimerSpec

	WantedBy []string
}

// ServiceSpec is the [Service] section.
type ServiceSpec struct {
	Type                   ServiceType
	ExecStart              []string // argv: executable then arguments
	WorkingDirectory       string
	Environment            []EnvVar
	Restart                RestartPolicy
	RestartSec             time.Duration
	TimeoutStartSec        time.Duration
	TimeoutStopSec         time.Duration
	NotifyAccess           NotifyAccess
	WatchdogSec            time.Duration
	WatchdogMode           WatchdogMode
	WatchdogEndpoint       string
	WatchdogExpectedStatus int
	WatchdogAddr           string // host:port for tcp/http (loopback)
	WatchdogURL            string // http(s) URL for WatchdogMode=http

	RestartSecSet             bool
	TimeoutStartSecSet        bool
	TimeoutStopSecSet         bool
	WatchdogSecSet            bool
	WatchdogEndpointSet       bool
	WatchdogExpectedStatusSet bool
}

// NeedsNotifyPipe reports whether the unit process should receive
// WINUNIT_NOTIFY_PIPE (Type=notify or WatchdogMode=notify).
func (s *ServiceSpec) NeedsNotifyPipe() bool {
	if s == nil {
		return false
	}
	if s.Type == TypeNotify {
		return true
	}
	return s.WatchdogEnabled() && s.WatchdogMode == WatchdogModeNotify
}

// WatchdogEnabled reports a positive WatchdogSec=.
func (s *ServiceSpec) WatchdogEnabled() bool {
	if s == nil {
		return false
	}
	return s.WatchdogSecSet && s.WatchdogSec > 0
}

// WatchdogProbeMode reports WatchdogMode=tcp or http.
func (s *ServiceSpec) WatchdogProbeMode() bool {
	if s == nil {
		return false
	}
	return s.WatchdogMode == WatchdogModeTCP || s.WatchdogMode == WatchdogModeHTTP
}

// TimerSpec is the [Timer] section.
type TimerSpec struct {
	OnBootSec       time.Duration
	OnStartupSec    time.Duration
	OnUnitActiveSec time.Duration
	OnCalendar      []timers.Calendar
	Persistent      bool
	Unit            string // activated unit; defaults to same basename .service

	OnBootSecSet       bool
	OnStartupSecSet    bool
	OnUnitActiveSecSet bool
}

// Severity is an issue level collected during parse/verify.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue is a parse or verify diagnostic.
type Issue struct {
	Path     string
	Line     int
	Severity Severity
	Message  string
}

func (i Issue) String() string {
	loc := i.Path
	if loc == "" {
		loc = "unit"
	}
	if i.Line > 0 {
		return fmt.Sprintf("%s:%d: %s: %s", loc, i.Line, i.Severity, i.Message)
	}
	return fmt.Sprintf("%s: %s: %s", loc, i.Severity, i.Message)
}

// Report is the result of parsing and verifying a unit file.
type Report struct {
	Unit   *Unit
	Issues []Issue
}

// HasError reports whether any error-severity issue is present.
func (r Report) HasError() bool {
	for _, iss := range r.Issues {
		if iss.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Errors returns error-severity issues.
func (r Report) Errors() []Issue {
	var out []Issue
	for _, iss := range r.Issues {
		if iss.Severity == SeverityError {
			out = append(out, iss)
		}
	}
	return out
}

// Warnings returns warning-severity issues.
func (r Report) Warnings() []Issue {
	var out []Issue
	for _, iss := range r.Issues {
		if iss.Severity == SeverityWarning {
			out = append(out, iss)
		}
	}
	return out
}
