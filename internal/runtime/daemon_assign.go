package runtime

import (
	"errors"
	"fmt"
)

// errAlreadyInJob is AssignProcessToJobObject ERROR_ACCESS_DENIED: the
// process already inherited the daemon job, or nested assignment is
// refused (known nesting). Callers of assignDaemonPID ignore this; other
// AssignPID errors are returned so strict ownership cannot fail silently
// (issue #33).
var errAlreadyInJob = errors.New("process already in job")

// assignDaemonPID assigns pid to the daemon job for strict ownership.
// Nil daemon is a no-op. errAlreadyInJob (ERROR_ACCESS_DENIED / known
// nesting) is ignored. Every other error is returned (no silent swallow).
func assignDaemonPID(daemon *DaemonJob, pid int) error {
	if daemon == nil {
		return nil
	}
	err := daemon.AssignPID(pid)
	if err == nil || errors.Is(err, errAlreadyInJob) {
		return nil
	}
	return fmt.Errorf("daemon job assign pid %d (not already-in-job/nesting): %w", pid, err)
}
