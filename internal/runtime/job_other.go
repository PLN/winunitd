//go:build !windows

package runtime

// DaemonJob is a no-op stand-in so protocol and manager tests can run on
// Linux. Strict ownership (KILL_ON_JOB_CLOSE) is implemented and tested in
// job_windows.go / job_windows_test.go.
type DaemonJob struct {
	closed bool
}

// OpenDaemonJob returns a stub job.
func OpenDaemonJob() (*DaemonJob, error) {
	return &DaemonJob{}, nil
}

// AssignPID is a no-op on non-Windows builds.
func (j *DaemonJob) AssignPID(pid int) error {
	return nil
}

// AssignSelf is a no-op on non-Windows builds.
func (j *DaemonJob) AssignSelf() error {
	return nil
}

// Close is a no-op on non-Windows builds besides marking the stub closed.
func (j *DaemonJob) Close() error {
	if j != nil {
		j.closed = true
	}
	return nil
}

// Closed reports whether Close has been called.
func (j *DaemonJob) Closed() bool {
	return j == nil || j.closed
}
