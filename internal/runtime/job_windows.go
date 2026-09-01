//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DaemonJob is the daemon-level Job Object for strict ownership
// (DESIGN.md §66). Processes assigned to it are terminated when the last
// handle is closed — including when winunitd.exe is killed.
//
// Per-unit jobs nest under this job on modern Windows (M5).
type DaemonJob struct {
	mu     sync.Mutex
	handle windows.Handle
}

// OpenDaemonJob creates an unnamed job with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.
// The current process is not assigned; AssignSelf or AssignPID is required.
// The daemon must keep the returned job alive until process exit so a crash
// still tears children down. Close after ordered unit stop; if this process
// is in the job, leftover children are terminated first so CloseHandle does
// not kill winunitd.
func OpenDaemonJob() (*DaemonJob, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create daemon job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
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
		return nil, fmt.Errorf("set daemon job kill-on-close: %w", err)
	}
	return &DaemonJob{handle: h}, nil
}

// AssignPID assigns an existing process to the daemon job.
func (j *DaemonJob) AssignPID(pid int) error {
	if j == nil {
		return fmt.Errorf("daemon job is closed")
	}
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	access := uint32(windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE)
	h, err := windows.OpenProcess(access, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return fmt.Errorf("daemon job is closed")
	}
	if err := windows.AssignProcessToJobObject(j.handle, h); err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return errAlreadyInJob
		}
		return fmt.Errorf("assign pid %d to daemon job: %w", pid, err)
	}
	return nil
}

// Assign attaches an already-open process handle to the daemon job.
// Same as UnitJob.Assign: use the CreateProcess handle, do not reopen by PID.
func (j *DaemonJob) Assign(process windows.Handle) error {
	if j == nil {
		return fmt.Errorf("daemon job is closed")
	}
	if process == 0 {
		return fmt.Errorf("process handle is closed")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return fmt.Errorf("daemon job is closed")
	}
	if err := windows.AssignProcessToJobObject(j.handle, process); err != nil {
		return fmt.Errorf("assign process to daemon job: %w", err)
	}
	return nil
}

// AssignSelf assigns the current process so future children inherit the job
// unless they break away (breakaway is not enabled). Nested per-unit jobs
// in M5 remain possible on modern Windows.
func (j *DaemonJob) AssignSelf() error {
	if j == nil {
		return fmt.Errorf("daemon job is closed")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return fmt.Errorf("daemon job is closed")
	}
	if err := windows.AssignProcessToJobObject(j.handle, windows.CurrentProcess()); err != nil {
		return fmt.Errorf("assign current process to daemon job: %w", err)
	}
	return nil
}

// Close closes the job handle so leftover children cannot outlive
// winunitd.exe (DESIGN.md §66). If this process is itself in the job,
// leftover children are terminated first and KILL_ON_JOB_CLOSE is cleared
// so CloseHandle does not kill the daemon after an ordered stop.
func (j *DaemonJob) Close() error {
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

	inSelf, err := isProcessInJob(windows.CurrentProcess(), h)
	if err == nil && inSelf {
		self := os.Getpid()
		for _, pid := range jobPIDs(h) {
			if pid == self {
				continue
			}
			_ = terminatePIDHandle(pid)
		}
		_ = clearKillOnClose(h)
	}
	if err := windows.CloseHandle(h); err != nil {
		return fmt.Errorf("close daemon job: %w", err)
	}
	return nil
}

// Closed reports whether Close has released the job handle.
func (j *DaemonJob) Closed() bool {
	if j == nil {
		return true
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.handle == 0
}

func jobPIDs(h windows.Handle) []int {
	if h == 0 {
		return nil
	}
	buf := make([]byte, 8+8*8)
	for {
		var retlen uint32
		err := windows.QueryInformationJobObject(
			h,
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
			return nil
		}
		n := *(*uint32)(unsafe.Pointer(&buf[4]))
		if n == 0 {
			return nil
		}
		ids := unsafe.Slice((*uintptr)(unsafe.Pointer(&buf[8])), int(n))
		out := make([]int, len(ids))
		for i, id := range ids {
			out[i] = int(id)
		}
		return out
	}
}

func terminatePIDHandle(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

func clearKillOnClose(h windows.Handle) error {
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: 0,
		},
	}
	_, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	return err
}

func (j *DaemonJob) killOnCloseEnabled() (bool, error) {
	if j == nil {
		return false, fmt.Errorf("daemon job is closed")
	}
	j.mu.Lock()
	h := j.handle
	j.mu.Unlock()
	if h == 0 {
		return false, fmt.Errorf("daemon job is closed")
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	err := windows.QueryInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	)
	if err != nil {
		return false, err
	}
	return info.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE != 0, nil
}

func (j *DaemonJob) inheritDup() (windows.Handle, error) {
	if j == nil {
		return 0, fmt.Errorf("daemon job is closed")
	}
	j.mu.Lock()
	h := j.handle
	j.mu.Unlock()
	if h == 0 {
		return 0, fmt.Errorf("daemon job is closed")
	}
	var dup windows.Handle
	err := windows.DuplicateHandle(
		windows.CurrentProcess(),
		h,
		windows.CurrentProcess(),
		&dup,
		0,
		true,
		windows.DUPLICATE_SAME_ACCESS,
	)
	if err != nil {
		return 0, err
	}
	return dup, nil
}
