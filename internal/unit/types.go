package unit

import (
	"fmt"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/eventlog"
	"github.com/PLN/winunitd/internal/pathwatch"
	"github.com/PLN/winunitd/internal/registry"
	"github.com/PLN/winunitd/internal/timers"
)

// Kind is the unit type implied by the file suffix.
type Kind string

const (
	KindService  Kind = "service"
	KindTimer    Kind = "timer"
	KindTarget   Kind = "target"
	KindRegistry Kind = "registry"
	KindEventLog Kind = "eventlog"
	KindPath     Kind = "path"
)

// ServiceType is a [Service] Type= value.
type ServiceType string

const (
	TypeSimple        ServiceType = "simple"
	TypeOneshot       ServiceType = "oneshot"
	TypeNotify        ServiceType = "notify"
	TypeSCM           ServiceType = "scm"
	TypeScheduledTask ServiceType = "scheduled-task"
)

// IsExternalProxy reports Type=scm or Type=scheduled-task.
func (t ServiceType) IsExternalProxy() bool {
	return t == TypeSCM || t == TypeScheduledTask
}

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

// PriorityClass is a [Service] PriorityClass= value (DESIGN.md §43).
// realtime is rejected at parse time.
type PriorityClass string

const (
	PriorityIdle        PriorityClass = "idle"
	PriorityBelowNormal PriorityClass = "below-normal"
	PriorityNormal      PriorityClass = "normal"
	PriorityAboveNormal PriorityClass = "above-normal"
	PriorityHigh        PriorityClass = "high"
)

// WindowsPriorityClass is the Win32 process priority class constant
// for this value (IDLE_PRIORITY_CLASS and siblings). Zero means unset.
func (p PriorityClass) WindowsPriorityClass() uint32 {
	switch p {
	case PriorityIdle:
		return 0x00000040
	case PriorityBelowNormal:
		return 0x00004000
	case PriorityNormal:
		return 0x00000020
	case PriorityAboveNormal:
		return 0x00008000
	case PriorityHigh:
		return 0x00000080
	default:
		return 0
	}
}

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
	BindsTo     []string
	PartOf      []string
	After       []string
	Before      []string

	// RequiresInteractiveSession skips the unit when no suitable
	// interactive session exists (DESIGN.md §15). SessionMode is not
	// implemented.
	RequiresInteractiveSession bool

	// StartLimitInterval / StartLimitBurst are [Unit] start rate limits
	// (DESIGN.md §20). Parse fills systemd-shaped defaults when omitted
	// (10s / 5). Burst 0 is unlimited.
	StartLimitInterval time.Duration
	StartLimitBurst    int

	Service   *ServiceSpec
	Timer     *TimerSpec
	Registry  *RegistrySpec
	EventLog  *EventLogSpec
	PathWatch *PathSpec

	WantedBy []string
}

// ServiceSpec is the [Service] section.
type ServiceSpec struct {
	Type                   ServiceType
	ServiceName            string   // Type=scm: existing SCM service (DESIGN.md §51)
	TaskName               string   // Type=scheduled-task: existing task path (DESIGN.md §52)
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

	// Job Object limits (DESIGN.md §43 R1). Zero / empty means omitted.
	MemoryMax     uint64
	ProcessLimit  uint32
	PriorityClass PriorityClass

	RestartSecSet             bool
	TimeoutStartSecSet        bool
	TimeoutStopSecSet         bool
	WatchdogSecSet            bool
	WatchdogEndpointSet       bool
	WatchdogExpectedStatusSet bool
	MemoryMaxSet              bool
	ProcessLimitSet           bool
	PriorityClassSet          bool
}

// HasJobResourceLimits reports MemoryMax= or ProcessLimit= (PriorityClass=
// does not produce a resource-limit failure).
func (s *ServiceSpec) HasJobResourceLimits() bool {
	if s == nil {
		return false
	}
	return s.MemoryMaxSet || s.ProcessLimitSet
}

// NeedsNotifyPipe reports whether the unit process should receive
// WINUNIT_NOTIFY_PIPE (Type=notify or WatchdogMode=notify).
func (s *ServiceSpec) NeedsNotifyPipe() bool {
	if s == nil || s.IsExternalProxy() {
		return false
	}
	if s.Type == TypeNotify {
		return true
	}
	return s.WatchdogEnabled() && s.WatchdogMode == WatchdogModeNotify
}

// WatchdogEnabled reports a positive WatchdogSec=. External proxy types
// (Type=scm, Type=scheduled-task) have no watchdog.
func (s *ServiceSpec) WatchdogEnabled() bool {
	if s == nil || s.IsExternalProxy() {
		return false
	}
	return s.WatchdogSecSet && s.WatchdogSec > 0
}

// IsSCM reports Type=scm (DESIGN.md §51).
func (s *ServiceSpec) IsSCM() bool {
	return s != nil && s.Type == TypeSCM
}

// IsScheduledTask reports Type=scheduled-task (DESIGN.md §52).
func (s *ServiceSpec) IsScheduledTask() bool {
	return s != nil && s.Type == TypeScheduledTask
}

// IsExternalProxy reports Type=scm or Type=scheduled-task: no CreateProcess,
// ExecStart, notify pipe, or WatchdogMode.
func (s *ServiceSpec) IsExternalProxy() bool {
	return s.IsSCM() || s.IsScheduledTask()
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

// RegistrySpec is the [Registry] section. The activated unit is always
// the same basename with a .service suffix (no Unit=).
type RegistrySpec struct {
	Changed []registry.Key
	Unit    string
}

// EventLogSpec is the [EventLog] section. The activated unit is always
// the same basename with a .service suffix (no Unit=).
type EventLogSpec struct {
	Triggers []eventlog.Trigger
	Unit     string
}

// PathSpec is the [Path] section. The activated unit is always
// the same basename with a .service suffix (no Unit=).
type PathSpec struct {
	Changed []pathwatch.Spec
	Unit    string
}

// CompanionService returns the basename .service for a companion unit
// (foo.timer / foo.registry / foo.eventlog / foo.path → foo.service).
// The result is a DESIGN.md §36 normalized name.
func CompanionService(name string) string {
	name = NormalizeName(name)
	base := name
	if i := strings.LastIndex(base, "."); i >= 0 {
		base = base[:i]
	}
	return NormalizeName(base + ".service")
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
