//go:build windows

package runtime

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DaemonJob is the daemon-level Job Object for strict ownership
// (DESIGN.md §66). Processes assigned to it are terminated when the last
// handle is closed — including when winunitd.exe is killed.
//
// Per-unit jobs nest under this job on modern Windows (M5).
type DaemonJob struct {
	closeWait daemonCloseWait
	exits     jobExitSet
	mu        sync.Mutex
	handle    windows.Handle
	broker    bool // immutable: only the SYSTEM broker permits explicit breakaway
}

// OpenDaemonJob creates an unnamed job with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.
// The current process is not assigned; AssignSelf or AssignPID is required.
// The daemon must keep the returned job alive until process exit so a crash
// still tears children down. Close after ordered unit stop; if this process
// is in the job, leftover children are terminated first so CloseHandle does
// not kill winunitd.
func OpenDaemonJob() (*DaemonJob, error) {
	return openDaemonJob(false)
}

// OpenBrokerJob permits the SYSTEM broker to launch into another session.
// Such managers have separate kill-on-close jobs owned by the broker; Windows
// cannot place processes from different sessions in the same job. Unit and
// user-manager jobs must continue to use OpenDaemonJob, which denies breakaway.
func OpenBrokerJob() (*DaemonJob, error) {
	return openDaemonJob(true)
}

func openDaemonJob(broker bool) (*DaemonJob, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create daemon job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if broker {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
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
	return &DaemonJob{handle: h, broker: broker}, nil
}

// AssignPID assigns an existing process to the daemon job.
func (j *DaemonJob) AssignPID(pid int) error {
	if j == nil {
		return fmt.Errorf("daemon job is closed")
	}
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	access := uint32(windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE | windows.PROCESS_QUERY_LIMITED_INFORMATION)
	h, err := windows.OpenProcess(access, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	return j.Assign(h)
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
	// Access denied can also mean a permissions or nesting failure. Only an
	// explicit membership result proves that assignment is already satisfied.
	member, err := isProcessInJob(process, j.handle)
	if err != nil {
		return fmt.Errorf("query daemon job membership: %w", err)
	}
	if member {
		return nil
	}
	if err := windows.AssignProcessToJobObject(j.handle, process); err != nil {
		return fmt.Errorf("assign process to daemon job: %w", err)
	}
	return nil
}

// assignDaemonProcess uses the creation handle while the child is suspended.
func assignDaemonProcess(j *DaemonJob, process windows.Handle) error {
	if j == nil {
		return nil
	}
	return j.Assign(process)
}

// AssignSelf assigns the current process so future children inherit the job
// unless explicitly launched out of a broker job. Nested per-unit jobs
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
	defer j.mu.Unlock()
	h := j.handle
	if h == 0 {
		return nil
	}
	inSelf, err := isProcessInJob(windows.CurrentProcess(), h)
	if err != nil {
		return fmt.Errorf("query daemon job before close: %w", err)
	}
	if inSelf {
		// Stop admission and retain handles before terminating descendants.
		// Never terminate this daemon or a process selected by a reused PID.
		self := os.Getpid()
		access := uint32(windows.SYNCHRONIZE | windows.PROCESS_TERMINATE | windows.PROCESS_QUERY_LIMITED_INFORMATION)
		if err := j.exits.prepareExcept(h, access, self); err != nil {
			return err
		}
		for _, process := range j.exits.handles {
			state, err := windows.WaitForSingleObject(process, 0)
			if err != nil {
				return fmt.Errorf("query daemon child exit: %w", err)
			}
			if state == windows.WAIT_OBJECT_0 {
				continue
			}
			member, err := isProcessInJob(process, h)
			if err != nil {
				return fmt.Errorf("verify daemon child membership: %w", err)
			}
			if !member {
				return fmt.Errorf("captured process is no longer a daemon job member")
			}
			if err := windows.TerminateProcess(process, 1); err != nil {
				return fmt.Errorf("terminate daemon child: %w", err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := j.exits.wait(ctx); err != nil {
			return err
		}
		if err := waitProcessListEmpty(ctx, func() ([]int, error) {
			ids, err := queryJobPIDs(h)
			if err != nil {
				return nil, err
			}
			var children []int
			for _, pid := range ids {
				if pid != self {
					children = append(children, pid)
				}
			}
			return children, nil
		}); err != nil {
			return err
		}
		if err := j.exits.close(); err != nil {
			return err
		}
		if err := clearKillOnClose(h); err != nil {
			return fmt.Errorf("clear daemon kill-on-close: %w", err)
		}
	}
	if err := windows.CloseHandle(h); err != nil {
		return fmt.Errorf("close daemon job: %w", err)
	}
	j.handle = 0
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

func queryJobPIDs(h windows.Handle) ([]int, error) {
	if h == 0 {
		return nil, fmt.Errorf("job handle is closed")
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

func clearKillOnClose(h windows.Handle) error {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return err
	}
	info.BasicLimitInformation.LimitFlags &^= windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if info.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_TIME != 0 {
		info.BasicLimitInformation.LimitFlags &^= windows.JOB_OBJECT_LIMIT_JOB_TIME
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PRESERVE_JOB_TIME
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
	defer j.mu.Unlock()
	h := j.handle
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
	defer j.mu.Unlock()
	h := j.handle
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
