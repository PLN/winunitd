package runtime

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/PLN/winunitd/internal/unit"
)

// ntstatusErrorSeverity is the NTSTATUS severity mask. Codes with the top
// two bits set (>= 0xC0000000) are error-severity NTSTATUS values used as
// process exit codes on crash (STATUS_ACCESS_VIOLATION 0xC0000005, etc.).
const ntstatusErrorSeverity = uint32(0xC0000000)

// ExitStatus is a process exit. Wait returns nil for code 0 and *ExitStatus
// for any other code, including NTSTATUS crash codes (>= 0xC0000000).
// SignalEquivalent classifies those crash codes as DESIGN.md §44
// signal-equivalent.
type ExitStatus struct {
	Code uint32
}

func (e *ExitStatus) Error() string {
	if e == nil {
		return "exit status 0"
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

// Failed reports a non-zero exit.
func (e *ExitStatus) Failed() bool {
	return e != nil && e.Code != 0
}

// SignalEquivalent reports an NTSTATUS error-severity exit
// (DESIGN.md §44 signal-equivalent).
func (e *ExitStatus) SignalEquivalent() bool {
	return e != nil && e.Code >= ntstatusErrorSeverity
}

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
	// bounds waiting for the process to exit. Type=notify ignores this
	// (the manager bounds READY=1 with TimeoutStartSec). Zero means no extra timeout.
	TimeoutStart time.Duration
	// Limits are applied to the unit Job Object (whole tree). Zero is today's job.
	Limits JobLimits
}

// Job is a per-unit Job Object (DESIGN.md §5.1). Closing or killing it
// tears down the whole process tree. Breakaway is not permitted.
type Job interface {
	Contains(pid int) (bool, error)
	PIDs() ([]int, error)
	Kill() error
	Close() error
	LimitFlags() (uint32, error)
	QueryLimits() (JobObjectLimits, error)
	// ResourceLimitC is closed when MemoryMax= or ProcessLimit= is hit.
	// Nil when those limits are omitted.
	ResourceLimitC() <-chan struct{}
	ResourceLimitHit() bool
}

// Process is one started unit invocation.
type Process interface {
	PID() int
	Alive() bool
	Job() Job
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Wait(ctx context.Context) error
	// ExitCode is the main-process exit after Wait has observed it.
	ExitCode() (code uint32, exited bool)
	// Stop kills the unit job (whole tree) and waits up to timeout for the
	// main process to exit.
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
