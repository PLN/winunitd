package runtime

import "errors"

var (
	// ErrNoProcess means OpenProcessProbe found no process with that ID.
	ErrNoProcess = errors.New("no such process")
	// ErrProcessAccess means the caller may not query that process.
	ErrProcessAccess = errors.New("process query access denied")
	// ErrJobClosed means a unit job was released before a membership query.
	ErrJobClosed = errors.New("unit job is closed")
)

// ProcessMember is a Job that can test whether a probed process is assigned
// to it, directly or through a nested job.
type ProcessMember interface {
	HasProcess(*ProcessProbe) (bool, error)
}
