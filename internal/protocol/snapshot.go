package protocol

// SnapshotResult is one immutable manager-local decision view. It contains no
// fresh process, proxy, journal, timer-engine or separate user-host observations.
type SnapshotResult struct {
	ManagerID  string            `json:"managerId"`
	Sequence   uint64            `json:"sequence"` // capture order, not a lifecycle revision
	CapturedAt string            `json:"capturedAt"`
	Machine    MachineStatus     `json:"machine"`
	Units      []UnitSnapshot    `json:"units"`
	Operations []OperationResult `json:"operations"` // active operations only
}

type UnitSnapshot struct {
	RestartAttempt           uint32   `json:"restartAttempt,omitempty"`
	RestartDelaySec          float64  `json:"restartDelaySec,omitempty"`
	Health                   string   `json:"health,omitempty"`
	ProbeFailures            int      `json:"probeFailures,omitempty"`
	Error                    string   `json:"error,omitempty"`
	Reason                   string   `json:"reason,omitempty"`
	NativeObservationError   string   `json:"nativeObservationError,omitempty"`
	Name                     string   `json:"name"`
	LoadState                string   `json:"loadState"`
	ActiveState              string   `json:"activeState"`
	SubState                 string   `json:"subState,omitempty"`
	Enabled                  bool     `json:"enabled"`
	MainPID                  int      `json:"mainPid,omitempty"`
	InvocationID             string   `json:"invocationId,omitempty"`
	ConfigRevision           string   `json:"configRevision,omitempty"`
	InvocationConfigRevision string   `json:"invocationConfigRevision,omitempty"`
	ArmedConfigRevision      string   `json:"armedConfigRevision,omitempty"`
	LastOperationID          string   `json:"lastOperationId,omitempty"`
	PendingCleanup           []string `json:"pendingCleanup,omitempty"`
}
