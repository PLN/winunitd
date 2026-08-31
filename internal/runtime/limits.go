package runtime

import "github.com/PLN/winunitd/internal/unit"

// Win32 process priority class constants (winbase.h). Defined here so
// Linux stubs and parse-side tests do not import golang.org/x/sys/windows.
const (
	PriorityIdle        = 0x00000040
	PriorityBelowNormal = 0x00004000
	PriorityNormal      = 0x00000020
	PriorityAboveNormal = 0x00008000
	PriorityHigh        = 0x00000080
)

// JobLimits are optional per-unit Job Object limits (DESIGN.md §43 R1).
// Zero values mean omitted — today's job (KILL_ON_JOB_CLOSE only).
type JobLimits struct {
	MemoryMax     uint64 // JOB_OBJECT_LIMIT_JOB_MEMORY
	ProcessLimit  uint32 // JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	PriorityClass uint32 // IDLE_PRIORITY_CLASS and siblings
}

// WatchViolations reports MemoryMax= or ProcessLimit= (completion-port events).
func (l JobLimits) WatchViolations() bool {
	return l.MemoryMax > 0 || l.ProcessLimit > 0
}

// JobObjectLimits is a queried snapshot of JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type JobObjectLimits struct {
	LimitFlags    uint32
	JobMemory     uint64
	PeakJobMemory uint64
	ProcessLimit  uint32
	PriorityClass uint32
}

// JobLimitsFromSpec copies parsed [Service] resource directives onto a
// CreateProcess StartSpec. Type=scm never reaches CreateProcess.
func JobLimitsFromSpec(svc *unit.ServiceSpec) JobLimits {
	if svc == nil {
		return JobLimits{}
	}
	var lim JobLimits
	if svc.MemoryMaxSet {
		lim.MemoryMax = svc.MemoryMax
	}
	if svc.ProcessLimitSet {
		lim.ProcessLimit = svc.ProcessLimit
	}
	if svc.PriorityClassSet {
		lim.PriorityClass = svc.PriorityClass.WindowsPriorityClass()
	}
	return lim
}
