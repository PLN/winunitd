package protocol

// Control methods match the winctl verbs (DESIGN.md §13, §74).
const (
	MethodStart        = "start"
	MethodStop         = "stop"
	MethodRestart      = "restart"
	MethodStatus       = "status"
	MethodEnable       = "enable"
	MethodDisable      = "disable"
	MethodListUnits    = "list-units"
	MethodListTimers   = "list-timers"
	MethodLogs         = "logs"
	MethodDaemonReload = "daemon-reload"
	MethodVerify       = "verify"
)

// Methods is the full set of control verbs.
var Methods = []string{
	MethodStart,
	MethodStop,
	MethodRestart,
	MethodStatus,
	MethodEnable,
	MethodDisable,
	MethodListUnits,
	MethodListTimers,
	MethodLogs,
	MethodDaemonReload,
	MethodVerify,
}

var knownMethods = func() map[string]bool {
	m := make(map[string]bool, len(Methods))
	for _, name := range Methods {
		m[name] = true
	}
	return m
}()

// KnownMethod reports whether name is a control verb.
func KnownMethod(name string) bool {
	return knownMethods[name]
}

// UnitParams is the body for verbs that take a unit name.
type UnitParams struct {
	Unit string `json:"unit"`
}

// ListUnitsParams is the body for list-units.
type ListUnitsParams struct{}

// ListUnitsResult is the list-units payload.
type ListUnitsResult struct {
	Units []UnitStatus `json:"units"`
}

// ListTimersParams is the body for list-timers.
type ListTimersParams struct{}

// ListTimersResult is the list-timers payload.
type ListTimersResult struct {
	Timers []TimerStatus `json:"timers"`
}

// TimerStatus is one timer in list-timers.
type TimerStatus struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path,omitempty"`
	LoadState   string `json:"loadState"`
	ActiveState string `json:"activeState"`
	Enabled     bool   `json:"enabled"`
	Unit        string `json:"unit,omitempty"` // activated service
	Next        string `json:"next,omitempty"`
	Last        string `json:"last,omitempty"`
}

// StatusParams is the body for status. An empty Unit requests machine status.
type StatusParams struct {
	Unit string `json:"unit,omitempty"`
}

// StatusResult is either machine status or a single unit.
type StatusResult struct {
	Machine *MachineStatus `json:"machine,omitempty"`
	Unit    *UnitStatus    `json:"unit,omitempty"`
}

// MachineStatus is the daemon-wide view (DESIGN.md §46).
type MachineStatus struct {
	State        string `json:"state"`
	UnitsLoaded  int    `json:"unitsLoaded"`
	UnitsActive  int    `json:"unitsActive"`
	UnitsFailed  int    `json:"unitsFailed"`
	TimersLoaded int    `json:"timersLoaded"`
}

// UnitStatus is one loaded unit (DESIGN.md §45). MainPID is set when the
// unit has a live process. Resource metrics are not reported.
type UnitStatus struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind"`
	Path        string `json:"path,omitempty"`
	LoadState   string `json:"loadState"`
	ActiveState string `json:"activeState"`
	Enabled     bool   `json:"enabled"`
	MainPID     int    `json:"mainPid,omitempty"`
	Error       string `json:"error,omitempty"`
	Next        string `json:"next,omitempty"`
	Last        string `json:"last,omitempty"`
}

// UnitResult is the payload for start, stop, and restart.
type UnitResult struct {
	Unit        string `json:"unit"`
	ActiveState string `json:"activeState"`
	Error       string `json:"error,omitempty"`
}

// EnableResult is the payload for enable and disable.
type EnableResult struct {
	Unit    string   `json:"unit"`
	Enabled bool     `json:"enabled"`
	Targets []string `json:"targets,omitempty"`
}

// LogsParams is the body for logs.
type LogsParams struct {
	Unit   string `json:"unit"`
	Follow bool   `json:"follow,omitempty"`
	Since  string `json:"since,omitempty"`
}

// LogsResult is a snapshot of stored journal entries. Follow is ignored
// (no streaming RPC); Since is reserved.
type LogsResult struct {
	Unit    string     `json:"unit"`
	Entries []LogEntry `json:"entries"`
}

// LogEntry is one journal line (DESIGN.md §22).
type LogEntry struct {
	Timestamp    string `json:"timestamp,omitempty"`
	Unit         string `json:"unit,omitempty"`
	PID          int    `json:"pid,omitempty"`
	Stream       string `json:"stream,omitempty"`
	Message      string `json:"message"`
	InvocationID string `json:"invocationId,omitempty"`
}

// DaemonReloadParams is the body for daemon-reload.
type DaemonReloadParams struct{}

// DaemonReloadResult is the daemon-reload payload (DESIGN.md §33).
type DaemonReloadResult struct {
	Loaded int      `json:"loaded"`
	Errors []string `json:"errors,omitempty"`
	Cycle  string   `json:"cycle,omitempty"`
}

// VerifyParams is the body for verify of a loaded unit (by name).
// Path-based verify stays in winctl and does not use the pipe.
type VerifyParams struct {
	Unit string `json:"unit"`
}

// VerifyResult is the verify payload.
type VerifyResult struct {
	Name   string  `json:"name"`
	OK     bool    `json:"ok"`
	Issues []Issue `json:"issues,omitempty"`
}

// Issue is a parse/verify diagnostic in the protocol.
type Issue struct {
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}
