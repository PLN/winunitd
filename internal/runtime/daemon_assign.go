package runtime

import "fmt"

// assignDaemonPID assigns an existing process to the daemon job. Already-owned
// processes succeed only after explicit membership verification on Windows.
func assignDaemonPID(daemon *DaemonJob, pid int) error {
	if daemon == nil {
		return nil
	}
	if err := daemon.AssignPID(pid); err != nil {
		return fmt.Errorf("daemon job assign pid %d: %w", pid, err)
	}
	return nil
}
