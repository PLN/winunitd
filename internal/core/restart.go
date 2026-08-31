package core

import "github.com/PLN/winunitd/internal/unit"

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
)

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
	default:
		return "exit-kind"
	}
}

// ShouldRestart reports whether Restart= launches the unit again after the
// main process has exited. There is no StartLimitBurst / StartLimitInterval.
//
//	no:          never
//	always:      any exit, including 0 and watchdog
//	on-failure:  non-zero exit, crash/signal-style failure, and watchdog
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
