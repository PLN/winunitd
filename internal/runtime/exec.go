package runtime

import (
	"context"
	"io"
	"time"

	"github.com/PLN/winunitd/internal/unit"
)

// StartSpec is a CreateProcess request for one unit invocation.
type StartSpec struct {
	Unit string
	Type unit.ServiceType
	Argv []string
	Dir  string
	// Env is the full environment block (KEY=value). Nil means inherit the
	// manager process environment.
	Env []string
	// TimeoutStart bounds CreateProcess for Type=simple. For Type=oneshot it
	// bounds waiting for the process to exit. Zero means no extra timeout.
	TimeoutStart time.Duration
}

// Job is a per-unit Job Object (DESIGN.md §5.1). Closing or killing it
// tears down the whole process tree. Breakaway is not permitted.
type Job interface {
	Contains(pid int) (bool, error)
	PIDs() ([]int, error)
	Kill() error
	Close() error
	LimitFlags() (uint32, error)
}

// Process is one started unit invocation.
type Process interface {
	PID() int
	Alive() bool
	Job() Job
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait(ctx context.Context) error
	// Stop kills the unit job (whole tree) and waits up to timeout for the
	// main process to exit. TimeoutStopSec wait-then-kill stays crude (M6
	// owns restart).
	Stop(timeout time.Duration) error
	Close() error
}

// Launcher starts a unit into its own Job Object.
type Launcher interface {
	Start(ctx context.Context, spec StartSpec) (Process, error)
}

// NewLauncher returns the platform launcher. daemon, if non-nil, is the M4
// daemon job: unit processes are assigned to it so they nest under strict
// ownership on modern Windows.
func NewLauncher(daemon *DaemonJob) Launcher {
	return newLauncher(daemon)
}

// DefaultLauncher is NewLauncher(nil).
func DefaultLauncher() Launcher {
	return NewLauncher(nil)
}
