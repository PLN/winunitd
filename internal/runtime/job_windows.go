//go:build windows

package runtime

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DaemonJob is the daemon-level Job Object for strict ownership
// (DESIGN.md §66). Processes assigned to it are terminated when the last
// handle is closed — including when winunitd.exe is killed.
//
// Per-unit jobs, KillMode, and breakaway policy are M5.
type DaemonJob struct {
	handle windows.Handle
}

// OpenDaemonJob creates an unnamed job with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.
// The current process is not assigned; AssignSelf or AssignPID is required.
// The daemon must keep the returned job alive until process exit so a crash
// still tears children down. Do not Close the job while the daemon is in it:
// that would terminate winunitd itself.
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
	if j == nil || j.handle == 0 {
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
	if err := windows.AssignProcessToJobObject(j.handle, h); err != nil {
		return fmt.Errorf("assign pid %d to daemon job: %w", pid, err)
	}
	return nil
}

// AssignSelf assigns the current process so future children inherit the job
// unless they break away (breakaway is not enabled). Nested per-unit jobs
// in M5 remain possible on modern Windows.
func (j *DaemonJob) AssignSelf() error {
	if j == nil || j.handle == 0 {
		return fmt.Errorf("daemon job is closed")
	}
	if err := windows.AssignProcessToJobObject(j.handle, windows.CurrentProcess()); err != nil {
		return fmt.Errorf("assign current process to daemon job: %w", err)
	}
	return nil
}

// Close closes the job handle. With KILL_ON_JOB_CLOSE, remaining assigned
// processes are terminated. The daemon host does not Close on a clean SCM
// stop (M10); process death still releases the handle.
func (j *DaemonJob) Close() error {
	if j == nil || j.handle == 0 {
		return nil
	}
	h := j.handle
	j.handle = 0
	if err := windows.CloseHandle(h); err != nil {
		return fmt.Errorf("close daemon job: %w", err)
	}
	return nil
}

func (j *DaemonJob) killOnCloseEnabled() (bool, error) {
	if j == nil || j.handle == 0 {
		return false, fmt.Errorf("daemon job is closed")
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	err := windows.QueryInformationJobObject(
		j.handle,
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
