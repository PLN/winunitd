//go:build windows

package runtime

import (
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	jobMsgActiveProcessLimit = 3
	jobMsgProcessMemoryLimit = 9
	jobMsgJobMemoryLimit     = 10
	jobMsgNotificationLimit  = 11
)

type jobAssociateCompletionPort struct {
	CompletionKey  uintptr
	CompletionPort windows.Handle
}

// UnitJob is a per-unit Job Object. It is created with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE and without BREAKAWAY_OK /
// SILENT_BREAKAWAY_OK, so children cannot leave the job (DESIGN.md §5.2).
type UnitJob struct {
	mu      sync.Mutex
	handle  windows.Handle
	iocp    windows.Handle
	limitCh chan struct{}
	hit     atomic.Uint32
	limits  JobLimits
}

// OpenUnitJob creates an unnamed unit job. Nested assignment under the
// daemon job is done by AssignProcessToJobObject after CreateProcess
// (modern Windows).
func OpenUnitJob() (*UnitJob, error) {
	return OpenUnitJobWith(JobLimits{})
}

// OpenUnitJobWith creates a unit job and applies optional R1/R2 limits
// (DESIGN.md §43). KILL_ON_JOB_CLOSE is always set; breakaway is not.
// CPUWeight=/CPUQuota= use JobObjectCpuRateControlInformation on this job
// (not the daemon job). IoPriority= is recorded here and applied to the
// process after job assignment.
func OpenUnitJobWith(lim JobLimits) (*UnitJob, error) {
	if lim.CPUWeight > 0 && lim.CPURate > 0 {
		return nil, fmt.Errorf("configuration: CPUWeight and CPUQuota cannot both be set")
	}
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create unit job: %w", err)
	}
	// Empty JobLimits is today's job: KILL_ON_JOB_CLOSE only. Do not set
	// BREAKAWAY_OK, SILENT_BREAKAWAY_OK, or PRIORITY_CLASS unless PriorityClass=
	// was given. Windows QueryInformationJobObject still fills PriorityClass
	// with NORMAL_PRIORITY_CLASS; QueryLimits reports 0 unless the flag is set.
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	flags := uint32(windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE)
	if lim.MemoryMax > 0 {
		flags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = uintptr(lim.MemoryMax)
	}
	if lim.ProcessLimit > 0 {
		flags |= windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
		info.BasicLimitInformation.ActiveProcessLimit = lim.ProcessLimit
	}
	if lim.PriorityClass != 0 {
		flags |= windows.JOB_OBJECT_LIMIT_PRIORITY_CLASS
		info.BasicLimitInformation.PriorityClass = lim.PriorityClass
	}
	info.BasicLimitInformation.LimitFlags = flags
	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("set unit job limits: %w", err)
	}
	if err := setJobCPURate(h, lim); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	j := &UnitJob{handle: h, limits: lim}
	if lim.WatchViolations() {
		if err := j.startLimitWatch(); err != nil {
			_ = windows.CloseHandle(h)
			return nil, err
		}
	}
	return j, nil
}

// Assign attaches an existing process to the unit job. The process is
// typically CREATE_SUSPENDED so it cannot spawn children before assignment.
func (j *UnitJob) Assign(process windows.Handle) error {
	if j == nil {
		return fmt.Errorf("unit job is closed")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return fmt.Errorf("unit job is closed")
	}
	if err := windows.AssignProcessToJobObject(j.handle, process); err != nil {
		return fmt.Errorf("assign process to unit job: %w", err)
	}
	return nil
}

// Contains reports whether pid is in this unit job.
func (j *UnitJob) Contains(pid int) (bool, error) {
	if pid <= 0 {
		return false, fmt.Errorf("invalid pid %d", pid)
	}
	if j == nil {
		return false, fmt.Errorf("unit job is closed")
	}
	ph, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(ph)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return false, fmt.Errorf("unit job is closed")
	}
	return isProcessInJob(ph, j.handle)
}

// PIDs lists process IDs currently assigned to the job.
func (j *UnitJob) PIDs() ([]int, error) {
	if j == nil {
		return nil, fmt.Errorf("unit job is closed")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return nil, fmt.Errorf("unit job is closed")
	}
	buf := make([]byte, 8+8*8)
	for {
		var retlen uint32
		err := windows.QueryInformationJobObject(
			j.handle,
			windows.JobObjectBasicProcessIdList,
			uintptr(unsafe.Pointer(&buf[0])),
			uint32(len(buf)),
			&retlen,
		)
		if err == windows.ERROR_MORE_DATA {
			buf = make([]byte, len(buf)*2)
			continue
		}
		if err != nil {
			return nil, err
		}
		n := *(*uint32)(unsafe.Pointer(&buf[4]))
		if n == 0 {
			return nil, nil
		}
		ids := unsafe.Slice((*uintptr)(unsafe.Pointer(&buf[8])), int(n))
		out := make([]int, len(ids))
		for i, id := range ids {
			out[i] = int(id)
		}
		return out, nil
	}
}

// Kill terminates every process in the job.
func (j *UnitJob) Kill() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return nil
	}
	if err := windows.TerminateJobObject(j.handle, 1); err != nil {
		return fmt.Errorf("terminate unit job: %w", err)
	}
	return nil
}

// Close closes the job handle. KILL_ON_JOB_CLOSE tears down remaining
// assigned processes.
func (j *UnitJob) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	h := j.handle
	iocp := j.iocp
	j.handle = 0
	j.iocp = 0
	j.mu.Unlock()
	if iocp != 0 {
		_ = windows.CloseHandle(iocp)
	}
	if h == 0 {
		return nil
	}
	if err := windows.CloseHandle(h); err != nil {
		return fmt.Errorf("close unit job: %w", err)
	}
	return nil
}

// LimitFlags returns JOBOBJECT_BASIC_LIMIT_INFORMATION.LimitFlags.
func (j *UnitJob) LimitFlags() (uint32, error) {
	if j == nil {
		return 0, fmt.Errorf("unit job is closed")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return 0, fmt.Errorf("unit job is closed")
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(
		j.handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	); err != nil {
		return 0, err
	}
	return info.BasicLimitInformation.LimitFlags, nil
}

// QueryLimits returns the job's extended limit snapshot.
func (j *UnitJob) QueryLimits() (JobObjectLimits, error) {
	if j == nil {
		return JobObjectLimits{}, fmt.Errorf("unit job is closed")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return JobObjectLimits{}, fmt.Errorf("unit job is closed")
	}
	got, err := queryJobLimits(j.handle)
	if err != nil {
		return JobObjectLimits{}, err
	}
	if j.limits.IoPrioritySet {
		got.IoPriority = j.limits.IoPriority
		got.IoPrioritySet = true
	}
	return got, nil
}

func queryJobLimits(h windows.Handle) (JobObjectLimits, error) {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	); err != nil {
		return JobObjectLimits{}, err
	}
	flags := info.BasicLimitInformation.LimitFlags
	got := JobObjectLimits{
		LimitFlags:    flags,
		PeakJobMemory: uint64(info.PeakJobMemoryUsed),
	}
	// Unused JO fields are not extra limits. Query still returns
	// NORMAL_PRIORITY_CLASS (32) when PRIORITY_CLASS was never set.
	if flags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY != 0 {
		got.JobMemory = uint64(info.JobMemoryLimit)
	}
	if flags&windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS != 0 {
		got.ProcessLimit = info.BasicLimitInformation.ActiveProcessLimit
	}
	if flags&windows.JOB_OBJECT_LIMIT_PRIORITY_CLASS != 0 {
		got.PriorityClass = info.BasicLimitInformation.PriorityClass
	}
	cpu, err := queryJobCPURate(h)
	if err == nil {
		got.CPUControlFlags = cpu.ControlFlags
		if cpu.ControlFlags&JobCPURateWeightBased != 0 {
			got.CPUWeight = cpu.Value
		}
		if cpu.ControlFlags&JobCPURateHardCap != 0 {
			got.CPURate = cpu.Value
		}
	}
	return got, nil
}

// jobCPURateControl is JOBOBJECT_CPU_RATE_CONTROL_INFORMATION (winnt.h).
// Value is the CpuRate/Weight union.
type jobCPURateControl struct {
	ControlFlags uint32
	Value        uint32
}

func setJobCPURate(h windows.Handle, lim JobLimits) error {
	var cpu jobCPURateControl
	switch {
	case lim.CPUWeight > 0:
		cpu.ControlFlags = JobCPURateEnable | JobCPURateWeightBased
		cpu.Value = lim.CPUWeight
	case lim.CPURate > 0:
		cpu.ControlFlags = JobCPURateEnable | JobCPURateHardCap
		cpu.Value = lim.CPURate
	default:
		return nil
	}
	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectCpuRateControlInformation,
		uintptr(unsafe.Pointer(&cpu)),
		uint32(unsafe.Sizeof(cpu)),
	); err != nil {
		return fmt.Errorf("configuration: set unit job cpu rate: %w", err)
	}
	return nil
}

func queryJobCPURate(h windows.Handle) (jobCPURateControl, error) {
	var cpu jobCPURateControl
	if err := windows.QueryInformationJobObject(
		h,
		windows.JobObjectCpuRateControlInformation,
		uintptr(unsafe.Pointer(&cpu)),
		uint32(unsafe.Sizeof(cpu)),
		nil,
	); err != nil {
		return jobCPURateControl{}, err
	}
	return cpu, nil
}

func setProcessIoPriority(process windows.Handle, prio uint32) error {
	v := prio
	if err := windows.NtSetInformationProcess(
		process,
		int32(windows.ProcessIoPriority),
		unsafe.Pointer(&v),
		uint32(unsafe.Sizeof(v)),
	); err != nil {
		return fmt.Errorf("configuration: set process IoPriority: %w", err)
	}
	return nil
}

// ResourceLimitC is closed when MemoryMax= or ProcessLimit= is hit.
func (j *UnitJob) ResourceLimitC() <-chan struct{} {
	if j == nil {
		return nil
	}
	return j.limitCh
}

// ResourceLimitHit reports a MemoryMax= or ProcessLimit= notification
// (or a PeakJobMemoryUsed that reached JobMemoryLimit).
func (j *UnitJob) ResourceLimitHit() bool {
	if j == nil {
		return false
	}
	if j.hit.Load() != 0 {
		return true
	}
	j.mu.Lock()
	h := j.handle
	j.mu.Unlock()
	if h == 0 {
		return false
	}
	got, err := queryJobLimits(h)
	if err != nil {
		return false
	}
	if got.JobMemory > 0 && got.PeakJobMemory >= got.JobMemory {
		j.markLimitHit()
		return true
	}
	return false
}

func (j *UnitJob) startLimitWatch() error {
	port, err := windows.CreateIoCompletionPort(windows.InvalidHandle, 0, 0, 1)
	if err != nil {
		return fmt.Errorf("create job completion port: %w", err)
	}
	assoc := jobAssociateCompletionPort{
		CompletionKey:  1,
		CompletionPort: port,
	}
	if _, err := windows.SetInformationJobObject(
		j.handle,
		windows.JobObjectAssociateCompletionPortInformation,
		uintptr(unsafe.Pointer(&assoc)),
		uint32(unsafe.Sizeof(assoc)),
	); err != nil {
		_ = windows.CloseHandle(port)
		return fmt.Errorf("associate job completion port: %w", err)
	}
	j.iocp = port
	j.limitCh = make(chan struct{})
	go j.limitLoop(port)
	return nil
}

func (j *UnitJob) limitLoop(port windows.Handle) {
	for {
		var qty uint32
		var key uintptr
		var ov *windows.Overlapped
		err := windows.GetQueuedCompletionStatus(port, &qty, &key, &ov, windows.INFINITE)
		if err != nil {
			return
		}
		switch qty {
		case jobMsgActiveProcessLimit, jobMsgProcessMemoryLimit, jobMsgJobMemoryLimit, jobMsgNotificationLimit:
			j.markLimitHit()
		}
	}
}

func (j *UnitJob) markLimitHit() {
	if j.hit.CompareAndSwap(0, 1) && j.limitCh != nil {
		close(j.limitCh)
	}
}

var procIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func isProcessInJob(process, job windows.Handle) (bool, error) {
	var in int32
	r1, _, e1 := procIsProcessInJob.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&in)))
	if r1 == 0 {
		if e1 != nil {
			return false, e1
		}
		return false, fmt.Errorf("IsProcessInJob failed")
	}
	return in != 0, nil
}
