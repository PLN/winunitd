//go:build windows

package runtime

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// UnitJob is a per-unit Job Object. It is created with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE and without BREAKAWAY_OK /
// SILENT_BREAKAWAY_OK, so children cannot leave the job (DESIGN.md §5.2).
type UnitJob struct {
	mu     sync.Mutex
	handle windows.Handle
}

// OpenUnitJob creates an unnamed unit job. Nested assignment under the
// daemon job is done by AssignProcessToJobObject after CreateProcess
// (modern Windows).
func OpenUnitJob() (*UnitJob, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create unit job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			// KILL_ON_JOB_CLOSE only. Do not set BREAKAWAY_OK or
			// SILENT_BREAKAWAY_OK — grandchildren must stay in the job.
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
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
	return &UnitJob{handle: h}, nil
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
	j.handle = 0
	j.mu.Unlock()
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
