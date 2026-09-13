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

// JobLimits are optional per-unit Job Object limits (DESIGN.md §43).
// Zero values mean omitted — today's job (KILL_ON_JOB_CLOSE only).
type JobLimits struct {
	MemoryMax     uint64 // JOB_OBJECT_LIMIT_JOB_MEMORY
	ProcessLimit  uint32 // JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	PriorityClass uint32 // IDLE_PRIORITY_CLASS and siblings
	CPUWeight     uint32 // Job Object weight 1–9 (JOB_OBJECT_CPU_RATE_CONTROL_WEIGHT_BASED)
	CPURate       uint32 // hundredths of a percent (JOB_OBJECT_CPU_RATE_CONTROL_HARD_CAP)
	IoPriority    uint32 // ProcessIoPriority ULONG (IO_PRIORITY_HINT)
	IoPrioritySet bool
}

// Job Object CPU rate ControlFlags (winnt.h). golang.org/x/sys/windows
// exposes the information class but not these flags.
const (
	JobCPURateEnable      = 0x1
	JobCPURateWeightBased = 0x2
	JobCPURateHardCap     = 0x4
)

// WatchViolations reports MemoryMax= or ProcessLimit= (completion-port events).
func (l JobLimits) WatchViolations() bool {
	return l.MemoryMax > 0 || l.ProcessLimit > 0
}

// JobObjectLimits is a queried snapshot of JOBOBJECT_EXTENDED_LIMIT_INFORMATION
// plus CPU rate control and recorded IoPriority (process-level).
type JobObjectLimits struct {
	LimitFlags      uint32
	JobMemory       uint64
	PeakJobMemory   uint64
	ProcessLimit    uint32
	PriorityClass   uint32
	CPUControlFlags uint32
	CPUWeight       uint32
	CPURate         uint32
	IoPriority      uint32
	IoPrioritySet   bool
}

// JobLimitsFromSpec copies parsed [Service] resource directives onto a
// CreateProcess StartSpec. Type=scm and Type=scheduled-task never reach CreateProcess.
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
	if svc.CPUWeightSet {
		lim.CPUWeight = unit.WindowsCPUWeight(svc.CPUWeight)
	}
	if svc.CPUQuotaSet {
		lim.CPURate = unit.WindowsCPURate(svc.CPUQuota)
	}
	if svc.WindowsCPUWeight != 0 {
		lim.CPUWeight = svc.WindowsCPUWeight
	}
	if svc.WindowsCPUQuota != 0 {
		lim.CPURate = unit.WindowsCPURate(svc.WindowsCPUQuota)
	}
	if svc.IoPrioritySet {
		lim.IoPriority = svc.IoPriority.WindowsIoPriority()
		lim.IoPrioritySet = true
	}
	return lim
}
