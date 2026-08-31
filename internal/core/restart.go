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
)

func (k ExitKind) String() string {
	switch k {
	case ExitSuccess:
		return "success"
	case ExitFailure:
		return "failure"
	case ExitAbnormal:
		return "abnormal"
	default:
		return "exit-kind"
	}
}

// ShouldRestart reports whether Restart= launches the unit again after the
// main process has exited. There is no StartLimitBurst / StartLimitInterval.
//
//	no:         never
//	always:     any exit, including 0
//	on-failure: non-zero exit and crash/signal-style failure
func ShouldRestart(policy unit.RestartPolicy, kind ExitKind) bool {
	switch policy {
	case unit.RestartAlways:
		return true
	case unit.RestartOnFailure:
		return kind != ExitSuccess
	default:
		return false
	}
}
