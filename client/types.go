package client

import "github.com/PLN/winunitd/internal/protocol"

// Protocol name and version spoken by this client.
const (
	ProtocolName    = protocol.Name
	ProtocolVersion = protocol.Version
)

// Control methods, as listed in CapabilitiesResult.Protocol.Methods.
const (
	MethodStart           = protocol.MethodStart
	MethodStop            = protocol.MethodStop
	MethodRestart         = protocol.MethodRestart
	MethodStatus          = protocol.MethodStatus
	MethodSnapshot        = protocol.MethodSnapshot
	MethodEnable          = protocol.MethodEnable
	MethodDisable         = protocol.MethodDisable
	MethodListUnits       = protocol.MethodListUnits
	MethodListTimers      = protocol.MethodListTimers
	MethodLogs            = protocol.MethodLogs
	MethodDaemonReload    = protocol.MethodDaemonReload
	MethodVerify          = protocol.MethodVerify
	MethodEnableLinger    = protocol.MethodEnableLinger
	MethodDisableLinger   = protocol.MethodDisableLinger
	MethodOperation       = protocol.MethodOperation
	MethodCancelOperation = protocol.MethodCancelOperation
	MethodMaintenance     = protocol.MethodMaintenance
	MethodCapabilities    = protocol.MethodCapabilities
	MethodUnitOfProcess   = protocol.MethodUnitOfProcess
)

// Error codes carried by *Error.
const (
	CodeInvalidRequest   = protocol.CodeInvalidRequest
	CodeMethodNotFound   = protocol.CodeMethodNotFound
	CodeInvalidParams    = protocol.CodeInvalidParams
	CodeNotFound         = protocol.CodeNotFound
	CodePermissionDenied = protocol.CodePermissionDenied
	CodeFailed           = protocol.CodeFailed
	CodeProtocolMismatch = protocol.CodeProtocolMismatch
	CodeBusy             = protocol.CodeBusy
)

// Feature names in CapabilitiesResult.Features.
const (
	FeatureExecStop       = protocol.FeatureExecStop
	FeatureRestartBackoff = protocol.FeatureRestartBackoff
	FeatureJobLimits      = protocol.FeatureJobLimits
	FeatureLingerS4U      = protocol.FeatureLingerS4U
)

// Error is a manager's refusal of a request.
type Error = protocol.Error

// Request parameters.
type (
	UnitParams          = protocol.UnitParams
	StatusParams        = protocol.StatusParams
	OperationParams     = protocol.OperationParams
	LogsParams          = protocol.LogsParams
	VerifyParams        = protocol.VerifyParams
	LingerParams        = protocol.LingerParams
	UnitOfProcessParams = protocol.UnitOfProcessParams
)

// Results and the types they contain.
type (
	CapabilitiesResult    = protocol.CapabilitiesResult
	ProtocolCapabilities  = protocol.ProtocolCapabilities
	StatusResult          = protocol.StatusResult
	MachineStatus         = protocol.MachineStatus
	UserManagerStatus     = protocol.UserManagerStatus
	UserRecoveryStatus    = protocol.UserRecoveryStatus
	DaemonEvent           = protocol.DaemonEvent
	MaintenanceResult     = protocol.MaintenanceResult
	UnitStatus            = protocol.UnitStatus
	RestartBudget         = protocol.RestartBudget
	NativeProxyStatus     = protocol.NativeProxyStatus
	TimerStatus           = protocol.TimerStatus
	TimerActivationStatus = protocol.TimerActivationStatus
	ListUnitsResult       = protocol.ListUnitsResult
	ListTimersResult      = protocol.ListTimersResult
	UnitResult            = protocol.UnitResult
	EnableResult          = protocol.EnableResult
	OperationResult       = protocol.OperationResult
	DaemonReloadResult    = protocol.DaemonReloadResult
	VerifyResult          = protocol.VerifyResult
	Issue                 = protocol.Issue
	LogsResult            = protocol.LogsResult
	LogEntry              = protocol.LogEntry
	LingerResult          = protocol.LingerResult
	UnitOfProcessResult   = protocol.UnitOfProcessResult
	SnapshotResult        = protocol.SnapshotResult
	UnitSnapshot          = protocol.UnitSnapshot
	UserHostSnapshot      = protocol.UserHostSnapshot
	UserSessionFailure    = protocol.UserSessionFailure
	UserSessionSnapshot   = protocol.UserSessionSnapshot
)
