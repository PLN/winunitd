//go:build !windows

package runtime

import (
	"fmt"
	"sync"
)

// DaemonJob is a no-op stand-in so protocol and manager tests can run on
// Linux. Strict ownership (KILL_ON_JOB_CLOSE) is implemented and tested in
// job_windows.go / job_windows_test.go.
type DaemonJob struct {
	closeWait daemonCloseWait
	mu        sync.Mutex
	closed    bool
}

// OpenDaemonJob returns a stub job.
func OpenDaemonJob() (*DaemonJob, error) {
	return &DaemonJob{}, nil
}

// OpenBrokerJob returns a stub job on non-Windows builds.
func OpenBrokerJob() (*DaemonJob, error) { return OpenDaemonJob() }

// AssignPID is a no-op on non-Windows builds unless the stub is closed
// or pid is invalid.
func (j *DaemonJob) AssignPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return fmt.Errorf("daemon job is closed")
	}
	return nil
}

// AssignSelf is a no-op on non-Windows builds unless the stub is closed.
func (j *DaemonJob) AssignSelf() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return fmt.Errorf("daemon job is closed")
	}
	return nil
}

// Close is a no-op on non-Windows builds besides marking the stub closed.
func (j *DaemonJob) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	j.closed = true
	j.mu.Unlock()
	return nil
}

// Closed reports whether Close has been called.
func (j *DaemonJob) Closed() bool {
	if j == nil {
		return true
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.closed
}
