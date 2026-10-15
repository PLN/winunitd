//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"math"
	"time"

	"golang.org/x/sys/windows"
)

// ProcessProbe holds one query handle to a process. While it is open Windows
// cannot reuse the process ID, so membership checks and the creation time
// describe the same process.
type ProcessProbe struct {
	pid     int
	handle  windows.Handle
	created time.Time
}

// OpenProcessProbe opens pid for limited query access and reads its creation
// time. Close releases the handle.
func OpenProcessProbe(pid int) (*ProcessProbe, error) {
	if pid <= 0 || int64(pid) > math.MaxUint32 {
		return nil, fmt.Errorf("invalid pid %d", pid)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		switch {
		case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
			return nil, fmt.Errorf("process %d: %w", pid, ErrNoProcess)
		case errors.Is(err, windows.ERROR_ACCESS_DENIED):
			return nil, fmt.Errorf("process %d: %w", pid, ErrProcessAccess)
		}
		return nil, fmt.Errorf("open process %d: %w", pid, err)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("process %d times: %w", pid, err)
	}
	return &ProcessProbe{pid: pid, handle: h, created: time.Unix(0, created.Nanoseconds()).UTC()}, nil
}

// PID is the probed process ID.
func (p *ProcessProbe) PID() int { return p.pid }

// Created is the process creation time from GetProcessTimes, exact to its
// 100-nanosecond FILETIME resolution.
func (p *ProcessProbe) Created() time.Time { return p.created }

// Close releases the process handle.
func (p *ProcessProbe) Close() error {
	if p == nil || p.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(p.handle)
	p.handle = 0
	return err
}
