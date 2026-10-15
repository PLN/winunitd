//go:build !windows

package runtime

import (
	"fmt"
	"time"
)

// ProcessProbe is a Windows process query handle; other builds cannot open one.
type ProcessProbe struct {
	pid     int
	created time.Time
}

// OpenProcessProbe is only available on Windows.
func OpenProcessProbe(pid int) (*ProcessProbe, error) {
	return nil, fmt.Errorf("process %d: process queries require Windows", pid)
}

// PID is the probed process ID.
func (p *ProcessProbe) PID() int { return p.pid }

// Created is the process creation time.
func (p *ProcessProbe) Created() time.Time { return p.created }

// Close does nothing on this build.
func (p *ProcessProbe) Close() error { return nil }
