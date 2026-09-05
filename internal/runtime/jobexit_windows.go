//go:build windows

package runtime

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobExitSet retains process handles across termination and retries. The job
// PID list can become empty before the corresponding process handles signal.
// Callers serialize prepare/wait/close and keep the job handle open.
type jobExitSet struct {
	ready   bool
	handles map[int]windows.Handle
}

func (s *jobExitSet) prepare(job windows.Handle) error {
	return s.prepareExcept(job, windows.SYNCHRONIZE, 0)
}

// prepareExcept supports a daemon contained in its own job. Its own process
// must not be captured for termination or waited on during shutdown.
func (s *jobExitSet) prepareExcept(job windows.Handle, access uint32, except int) error {
	if s.ready {
		return nil
	}
	if job == 0 {
		return fmt.Errorf("job closed before exit capture")
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil); err != nil {
		return fmt.Errorf("query job before termination: %w", err)
	}
	// Stop admission before enumerating: no child may appear between the
	// process snapshot and TerminateJobObject. Preserve all other limits.
	if limits.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_TIME != 0 {
		limits.BasicLimitInformation.LimitFlags &^= windows.JOB_OBJECT_LIMIT_JOB_TIME
		limits.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PRESERVE_JOB_TIME
	}
	limits.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	limits.BasicLimitInformation.ActiveProcessLimit = 0
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fmt.Errorf("close job process admission: %w", err)
	}
	ids, err := queryJobPIDs(job)
	if err != nil {
		return err
	}
	if s.handles == nil {
		s.handles = make(map[int]windows.Handle)
	}
	for _, pid := range ids {
		if pid == except {
			continue
		}
		if _, held := s.handles[pid]; held {
			continue
		}
		h, err := windows.OpenProcess(access, false, uint32(pid))
		if err == windows.ERROR_INVALID_PARAMETER {
			continue // process has already exited and its PID no longer exists
		}
		if err != nil {
			return fmt.Errorf("capture job process exit: %w", err)
		}
		// Unit termination targets the job. Daemon shutdown must verify
		// membership on this captured handle before individual termination;
		// PID reuse must never select an unrelated process for termination.
		s.handles[pid] = h
	}
	s.ready = true
	return nil
}

func (s *jobExitSet) wait(ctx context.Context) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		pending := false
		for _, h := range s.handles {
			state, err := windows.WaitForSingleObject(h, 0)
			if err != nil {
				return fmt.Errorf("confirm job process exit: %w", err)
			}
			if state != windows.WAIT_OBJECT_0 {
				pending = true
			}
		}
		if !pending {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for job process exit: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

func (s *jobExitSet) close() error {
	for pid, h := range s.handles {
		if err := windows.CloseHandle(h); err != nil {
			return fmt.Errorf("close captured process handle: %w", err)
		}
		delete(s.handles, pid)
	}
	return nil
}
