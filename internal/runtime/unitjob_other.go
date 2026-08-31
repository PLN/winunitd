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
	limits JobLimits
}

// OpenUnitJob returns a stub job.
func OpenUnitJob() (*UnitJob, error) {
	return OpenUnitJobWith(JobLimits{})
}

// OpenUnitJobWith records limits so QueryLimits can be asserted on Linux.
func OpenUnitJobWith(lim JobLimits) (*UnitJob, error) {
	return &UnitJob{limits: lim}, nil
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

// LimitFlags reports no Win32 flags on the stub.
func (j *UnitJob) LimitFlags() (uint32, error) {
	got, err := j.QueryLimits()
	if err != nil {
		return 0, err
	}
	return got.LimitFlags, nil
}

// QueryLimits returns recorded stub limits (not a real Job Object).
func (j *UnitJob) QueryLimits() (JobObjectLimits, error) {
	if j == nil {
		return JobObjectLimits{}, fmt.Errorf("unit job is closed")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return JobObjectLimits{}, fmt.Errorf("unit job is closed")
	}
	return JobObjectLimits{
		JobMemory:     j.limits.MemoryMax,
		ProcessLimit:  j.limits.ProcessLimit,
		PriorityClass: j.limits.PriorityClass,
	}, nil
}

// ResourceLimitC is always nil on the stub.
func (j *UnitJob) ResourceLimitC() <-chan struct{} { return nil }

// ResourceLimitHit is always false on the stub.
func (j *UnitJob) ResourceLimitHit() bool { return false }
