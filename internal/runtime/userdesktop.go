package runtime

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// UserDesktopHelperFlag is an internal SYSTEM bootstrap mode of the daemon.
const UserDesktopHelperFlag = "--user-desktop-helper"

// The desktop process is a launch resource, not the user manager's liveness
// authority. Keep its job and readiness read until cleanup is confirmed.
type userDesktopLease struct {
	mu       sync.Mutex
	helper   Process
	pending  io.Closer // empty child job until process creation transfers it
	readDone chan struct{}
	readErr  error
}

func (d *userDesktopLease) readReady(expected string, timeout time.Duration) error {
	d.readDone = make(chan struct{})
	reader := d.helper.Stdout()
	go func() {
		defer close(d.readDone)
		buf := make([]byte, len(expected))
		_, d.readErr = io.ReadFull(reader, buf)
		if d.readErr == nil && string(buf) != expected {
			d.readErr = errors.New("invalid desktop helper readiness")
		}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-d.readDone:
		return d.readErr
	case <-timer.C:
		return errors.New("desktop helper readiness timed out")
	}
}

func (d *userDesktopLease) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var result error
	if d.pending != nil {
		if err := d.pending.Close(); err != nil {
			result = err
		} else {
			d.pending = nil
		}
	}
	if d.helper != nil {
		if err := d.helper.Stop(5 * time.Second); err != nil {
			return errors.Join(result, fmt.Errorf("stop desktop helper: %w", err))
		}
		d.helper = nil
	}
	if d.readDone != nil {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-d.readDone:
			d.readDone = nil
		case <-timer.C:
			return errors.Join(result, errors.New("desktop helper readiness cleanup timed out"))
		}
	}
	return result
}
