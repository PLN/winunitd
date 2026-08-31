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
}

// OpenUnitJob creates an unnamed unit job. Nested assignment under the
// daemon job is done by AssignProcessToJobObject after CreateProcess
// (modern Windows).
func OpenUnitJob() (*UnitJob, error) {
	return OpenUnitJobWith(JobLimits{})
}

// OpenUnitJobWith creates a unit job and applies optional R1 limits
// (DESIGN.md §43). KILL_ON_JOB_CLOSE is always set; breakaway is not.
func OpenUnitJobWith(lim JobLimits) (*UnitJob, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create unit job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			// KILL_ON_JOB_CLOSE only unless R1 limits are set. Do not set
			// BREAKAWAY_OK or SILENT_BREAKAWAY_OK — grandchildren stay in the job.
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if lim.MemoryMax > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = uintptr(lim.MemoryMax)
	}
	if lim.ProcessLimit > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
		info.BasicLimitInformation.ActiveProcessLimit = lim.ProcessLimit
	}
	if lim.PriorityClass != 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PRIORITY_CLASS
		info.BasicLimitInformation.PriorityClass = lim.PriorityClass
	}
	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("set unit job limits: %w", err)
	}
	j := &UnitJob{handle: h}
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
	return queryJobLimits(j.handle)
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
	return JobObjectLimits{
		LimitFlags:    info.BasicLimitInformation.LimitFlags,
		JobMemory:     uint64(info.JobMemoryLimit),
		PeakJobMemory: uint64(info.PeakJobMemoryUsed),
		ProcessLimit:  info.BasicLimitInformation.ActiveProcessLimit,
		PriorityClass: info.BasicLimitInformation.PriorityClass,
	}, nil
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
