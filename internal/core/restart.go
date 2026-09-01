package core

import (
	"strings"

	"github.com/PLN/winunitd/internal/unit"
)

// ExitKind classifies a main-process wait result for Restart=.
type ExitKind int

const (
	// ExitSuccess is a clean exit with code 0.
	ExitSuccess ExitKind = iota
	// ExitFailure is a non-zero exit code.
	ExitFailure
	// ExitAbnormal is a crash, wait error, or signal-style failure.
	ExitAbnormal
	// ExitWatchdog is a missed WatchdogSec= heartbeat.
	ExitWatchdog
	// ExitResourceLimit is a Job Object MemoryMax= or ProcessLimit= hit.
	ExitResourceLimit
)

// ReasonResourceLimit is the winctl status reason for a Job Object limit
// (DESIGN.md §44).
const ReasonResourceLimit = "resource-limit"

// ReasonConfiguration is the winctl status reason for a unit that failed
// because its configuration cannot be applied (e.g. a missing registry key).
const ReasonConfiguration = "configuration"

// StatusReason maps a stored error string to a status Reason= value.
func StatusReason(err string) string {
	switch {
	case err == ReasonResourceLimit:
		return ReasonResourceLimit
	case err == ReasonStartLimit:
		return ReasonStartLimit
	case err == ReasonConfiguration || strings.HasPrefix(err, ReasonConfiguration+":") || strings.HasPrefix(err, ReasonConfiguration+" "):
		return ReasonConfiguration
	default:
		return ""
	}
}

func (k ExitKind) String() string {
	switch k {
	case ExitSuccess:
		return "success"
	case ExitFailure:
		return "failure"
	case ExitAbnormal:
		return "abnormal"
	case ExitWatchdog:
		return "watchdog"
	case ExitResourceLimit:
		return "resource-limit"
	default:
		return "exit-kind"
	}
}

// ShouldRestart reports whether Restart= launches the unit again after the
// main process has exited. StartLimitBurst / StartLimitIntervalSec are
// applied by the manager before scheduling that relaunch.
//
//	no:          never
//	always:      any exit, including 0, watchdog, and resource-limit
//	on-failure:  non-zero exit, crash/signal-style failure, watchdog, and resource-limit
//	on-watchdog: missed WatchdogSec= only
func ShouldRestart(policy unit.RestartPolicy, kind ExitKind) bool {
	switch policy {
	case unit.RestartAlways:
		return true
	case unit.RestartOnFailure:
		return kind != ExitSuccess
	case unit.RestartOnWatchdog:
		return kind == ExitWatchdog
	default:
		return false
	}
}
