package protocol

// SnapshotResult is one immutable decision view. System control additionally
// copies user-host ownership while both decision locks are held in fixed order.
// It contains no fresh process, proxy, journal or timer-engine observations.
type SnapshotResult struct {
	UserHost   *UserHostSnapshot `json:"userHost,omitempty"`
	ManagerID  string            `json:"managerId"`
	Sequence   uint64            `json:"sequence"` // capture order, not a lifecycle revision
	CapturedAt string            `json:"capturedAt"`
	Machine    MachineStatus     `json:"machine"`
	Units      []UnitSnapshot    `json:"units"`
	Operations []OperationResult `json:"operations"` // active operations only
}

type UnitSnapshot struct {
	NativeProxy              *NativeProxyStatus `json:"nativeProxy,omitempty"`
	RestartAttempt           uint32             `json:"restartAttempt,omitempty"`
	RestartDelaySec          float64            `json:"restartDelaySec,omitempty"`
	Health                   string             `json:"health,omitempty"`
	ProbeFailures            int                `json:"probeFailures,omitempty"`
	Error                    string             `json:"error,omitempty"`
	Reason                   string             `json:"reason,omitempty"`
	NativeObservationError   string             `json:"nativeObservationError,omitempty"`
	Name                     string             `json:"name"`
	LoadState                string             `json:"loadState"`
	ActiveState              string             `json:"activeState"`
	SubState                 string             `json:"subState,omitempty"`
	Enabled                  bool               `json:"enabled"`
	MainPID                  int                `json:"mainPid,omitempty"`
	InvocationID             string             `json:"invocationId,omitempty"`
	ConfigRevision           string             `json:"configRevision,omitempty"`
	InvocationConfigRevision string             `json:"invocationConfigRevision,omitempty"`
	ArmedConfigRevision      string             `json:"armedConfigRevision,omitempty"`
	LastOperationID          string             `json:"lastOperationId,omitempty"`
	PendingCleanup           []string           `json:"pendingCleanup,omitempty"`
}

// NativeProxyStatus describes the captured adapter contract, not queried native
// permissions or liveness. Requests can still fail in the owning Windows API.
type NativeProxyStatus struct {
	Kind              string   `json:"kind"`
	Target            string   `json:"target"`
	Owner             string   `json:"owner"`
	Capabilities      []string `json:"capabilities"`
	OwnsProcessTree   bool     `json:"ownsProcessTree"`
	CapturesOutput    bool     `json:"capturesOutput"`
	SupportsExecStop  bool     `json:"supportsExecStop"`
	SupportsJobLimits bool     `json:"supportsJobLimits"`
	ManagesDefinition bool     `json:"managesDefinition"`
}

// UserHostSnapshot contains accepted ownership, not queried process liveness.
// Its host-scoped instance IDs survive cleanup uncertainty and change on launch.
type UserHostSnapshot struct {
	HostID              string                `json:"hostId"`
	State               string                `json:"state"`
	AdmissionRevision   uint64                `json:"admissionRevision"`
	LingerRevision      uint64                `json:"lingerRevision"`
	SessionEpoch        uint64                `json:"sessionEpoch"`
	NativeWork          int                   `json:"nativeWork"`
	PendingTokenCleanup int                   `json:"pendingTokenCleanup"`
	Lingering           int                   `json:"lingering"`
	LingerState         string                `json:"lingerState,omitempty"`
	LingerError         string                `json:"lingerError,omitempty"`
	Instances           []UserManagerStatus   `json:"instances"`
	Recovery            []UserRecoveryStatus  `json:"recovery"`
	Sessions            []UserSessionSnapshot `json:"sessions"`
}

type UserSessionSnapshot struct {
	SessionID      uint32 `json:"sessionId"`
	SID            string `json:"sid,omitempty"`
	PendingRequest uint64 `json:"pendingRequest,omitempty"`
}
