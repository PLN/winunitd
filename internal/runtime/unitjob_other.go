//go:build !windows

package runtime

import (
	"fmt"
	"sync"
)

// UnitJob is a no-op stand-in so manager tests can run on Linux.
type UnitJob struct {
	mu     sync.Mutex
	closed bool
}

// OpenUnitJob returns a stub job.
func OpenUnitJob() (*UnitJob, error) {
	return &UnitJob{}, nil
}

// Contains is always false on non-Windows builds.
func (j *UnitJob) Contains(pid int) (bool, error) {
	if j == nil {
		return false, fmt.Errorf("unit job is closed")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return false, fmt.Errorf("unit job is closed")
	}
	return false, nil
}

// PIDs is empty on non-Windows builds.
func (j *UnitJob) PIDs() ([]int, error) {
	if j == nil {
		return nil, fmt.Errorf("unit job is closed")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil, fmt.Errorf("unit job is closed")
	}
	return nil, nil
}

// Kill is a no-op on non-Windows builds.
func (j *UnitJob) Kill() error {
	return nil
}

// Close is a no-op on non-Windows builds.
func (j *UnitJob) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	j.closed = true
	j.mu.Unlock()
	return nil
}

// LimitFlags reports no limits on the stub.
func (j *UnitJob) LimitFlags() (uint32, error) {
	if j == nil {
		return 0, fmt.Errorf("unit job is closed")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return 0, fmt.Errorf("unit job is closed")
	}
	return 0, nil
}
