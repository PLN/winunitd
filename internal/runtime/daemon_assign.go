package runtime

import "errors"

// errAlreadyInJob is AssignProcessToJobObject ERROR_ACCESS_DENIED: the
// process already inherited the daemon job, or nested assignment is
// refused. Callers of assignDaemonPID ignore this; other AssignPID
// errors must not be swallowed (issue #33).
var errAlreadyInJob = errors.New("process already in job")

// assignDaemonPID assigns pid to the daemon job for strict ownership.
// Nil daemon is a no-op. errAlreadyInJob is ignored; every other error
// is returned so a failed nest cannot fail closed silently.
func assignDaemonPID(daemon *DaemonJob, pid int) error {
	if daemon == nil {
		return nil
	}
	err := daemon.AssignPID(pid)
	if err == nil || errors.Is(err, errAlreadyInJob) {
		return nil
	}
	return err
}
