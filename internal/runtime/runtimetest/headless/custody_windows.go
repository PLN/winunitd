//go:build windows

package headless

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// custody is a kill-on-close job holding a launched process tree, so
// nothing the tree starts outlives the command that launched it.
type custody struct{ job windows.Handle }

// jobAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type jobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func newCustody() (*custody, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	return &custody{job: job}, nil
}

// start starts cmd suspended, assigns it to the job and resumes it, so no
// process it creates escapes the job. A process that cannot be placed is
// killed and reaped.
func (c *custody) start(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		return baseOnly(err)
	}
	pid := uint32(cmd.Process.Pid)
	err := c.adopt(pid)
	if err == nil {
		err = resumeProcess(pid)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return err
}

// adopt assigns a running process to the job. The caller holds the
// process's handle, so its PID cannot have been reused.
func (c *custody) adopt(pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.AssignProcessToJobObject(c.job, h)
}

// terminate ends every process in the job.
func (c *custody) terminate() { _ = windows.TerminateJobObject(c.job, 1) }

// end terminates the job, confirms within the bound that no process
// remains in it, and closes it.
func (c *custody) end(bound time.Duration) error {
	c.terminate()
	deadline := time.Now().Add(bound)
	var err error
	for {
		var acct jobAccounting
		qerr := windows.QueryInformationJobObject(c.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&acct)), uint32(unsafe.Sizeof(acct)), nil)
		if qerr == nil && acct.ActiveProcesses == 0 {
			break
		}
		if time.Now().After(deadline) {
			err = errors.New("processes in custody did not exit")
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.Join(err, windows.CloseHandle(c.job))
}

// resumeProcess resumes the threads of a process created suspended.
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, oerr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if oerr != nil {
			return oerr
		}
		_, rerr := windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if rerr != nil {
			return rerr
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	if resumed == 0 {
		return errors.New("the suspended process has no thread")
	}
	return nil
}
