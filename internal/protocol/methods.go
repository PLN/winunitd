package protocol

// Control methods match the winctl verbs (DESIGN.md §13, §74).
const (
	MethodStart         = "start"
	MethodStop          = "stop"
	MethodRestart       = "restart"
	MethodStatus        = "status"
	MethodEnable        = "enable"
	MethodDisable       = "disable"
	MethodListUnits     = "list-units"
	MethodListTimers    = "list-timers"
	MethodLogs          = "logs"
	MethodDaemonReload  = "daemon-reload"
	MethodVerify        = "verify"
	MethodEnableLinger  = "enable-linger"
	MethodDisableLinger = "disable-linger"
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
	MethodEnableLinger,
	MethodDisableLinger,
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

// LingerMethod reports whether name is an admin linger verb. These run
// on the system pipe only (not winctl --user).
func LingerMethod(name string) bool {
	return name == MethodEnableLinger || name == MethodDisableLinger
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
	ConfigRevision      string `json:"configRevision,omitempty"`
	ArmedConfigRevision string `json:"armedConfigRevision,omitempty"`

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
	ConfigRevision string `json:"configRevision,omitempty"`

	State        string `json:"state"`
	UnitsLoaded  int    `json:"unitsLoaded"`
	UnitsActive  int    `json:"unitsActive"`
	UnitsFailed  int    `json:"unitsFailed"`
	TimersLoaded int    `json:"timersLoaded"`
	UserManagers int    `json:"userManagers,omitempty"`
	Lingering    int    `json:"lingering,omitempty"`
}

// UnitStatus is one loaded unit (DESIGN.md §24, §45). MainPID is set when
// the unit has a live process. InvocationID is the last unit-start UUID.
// Resource metrics are not reported. CPUWeight/CPUQuota/IoPriority are the
// configured unit-file values when set (cheap; not a live Job Object query).
type UnitStatus struct {
	// ConfigRevision is empty when no loaded definition is accepted. The
	// invocation field identifies the last captured service definition, even
	// after stop. ArmedConfigRevision identifies the installed native watch or
	// armed timer separately; it clears when that ownership is released.
	ConfigRevision           string `json:"configRevision,omitempty"`
	InvocationConfigRevision string `json:"invocationConfigRevision,omitempty"`
	ArmedConfigRevision      string `json:"armedConfigRevision,omitempty"`

	Name                string `json:"name"`
	Description         string `json:"description,omitempty"`
	Kind                string `json:"kind"`
	Path                string `json:"path,omitempty"`
	LoadState           string `json:"loadState"`
	ActiveState         string `json:"activeState"`
	Enabled             bool   `json:"enabled"`
	MainPID             int    `json:"mainPid,omitempty"`
	InvocationID        string `json:"invocationId,omitempty"`
	Error               string `json:"error,omitempty"`
	LogDroppedRecords   uint64 `json:"logDroppedRecords,omitempty"`
	LogDroppedBytes     uint64 `json:"logDroppedBytes,omitempty"`
	LogStorageErrors    uint64 `json:"logStorageErrors,omitempty"`
	LogLastStorageError string `json:"logLastStorageError,omitempty"`
	Reason              string `json:"reason,omitempty"` // DESIGN.md §44
	Next                string `json:"next,omitempty"`
	Last                string `json:"last,omitempty"`
	// Configured [Service] R2 limits (unit file; not a live Job Object query).
	CPUWeight  uint32 `json:"cpuWeight,omitempty"`
	CPUQuota   uint32 `json:"cpuQuota,omitempty"` // N from CPUQuota=N%
	IoPriority string `json:"ioPriority,omitempty"`
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

// LogsParams is the body for logs (DESIGN.md §22).
//
// Since is a lower bound: RFC3339 (nano or second), YYYY-MM-DD (UTC
// midnight), a Go duration subtracted from now ("1h"), or "N <unit> ago".
// Empty means no lower bound. A non-empty value that does not parse is
// invalid-params (not ignored).
//
// Follow requests a short server wait for new lines after Cursor. There
// is no streaming RPC; winctl --follow polls. Cursor is opaque and comes
// from a previous LogsResult.
type LogsParams struct {
	Unit   string `json:"unit"`
	Follow bool   `json:"follow,omitempty"`
	Since  string `json:"since,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// LogsResult is a snapshot of stored journal entries (DESIGN.md §22).
// Cursor is passed back on the next page when More is true, or the next
// poll when following. LogEntry is the
// v=2 journal record (severity / session / user SID may be empty on
// historical v=1 lines).
type LogsResult struct {
	Unit    string     `json:"unit"`
	Entries []LogEntry `json:"entries"`
	Cursor  string     `json:"cursor,omitempty"`
	More    bool       `json:"more,omitempty"` // fetch the next page using Cursor
}

// LogEntry is one journal fragment. Continuation and Partial expose v=3
// fragment boundaries; older records default these fields to false.
type LogEntry struct {
	Timestamp    string `json:"timestamp,omitempty"`
	Unit         string `json:"unit,omitempty"`
	PID          int    `json:"pid,omitempty"`
	Stream       string `json:"stream,omitempty"`
	Message      string `json:"message"`
	InvocationID string `json:"invocationId,omitempty"`
	Severity     string `json:"severity,omitempty"`
	Session      string `json:"session,omitempty"`
	UserSID      string `json:"userSid,omitempty"`
	Continuation bool   `json:"continuation,omitempty"`
	Partial      bool   `json:"partial,omitempty"`
}

// DaemonReloadParams is the body for daemon-reload.
type DaemonReloadParams struct{}

// DaemonReloadResult is the daemon-reload payload (DESIGN.md §33).
type DaemonReloadResult struct {
	ConfigRevision string `json:"configRevision,omitempty"`

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

// LingerParams is the body for enable-linger and disable-linger.
// User is a username (DOMAIN\account) or an NT SID.
type LingerParams struct {
	User string `json:"user"`
}

// LingerResult is the payload for enable-linger and disable-linger.
type LingerResult struct {
	SID       string `json:"sid"`
	User      string `json:"user,omitempty"`
	Lingering bool   `json:"lingering"`
}
