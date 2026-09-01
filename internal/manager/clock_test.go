package manager

import (
	"context"
	"os"
	"path/filepath"
	rt "runtime"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
)

func TestClockTimeoutCancelStopsFakeTimer(t *testing.T) {
	t.Parallel()
	fk := timers.NewFake(time.Time{})
	m := &Manager{clk: fk.Clock()}
	_, cancel := m.clockTimeout(context.Background(), 5*time.Second)
	if !fk.Waiting() {
		t.Fatal("expected TimeoutStartSec timer")
	}
	cancel()
	if fk.Waiting() {
		t.Fatal("cancel must Stop the clock timer")
	}
}

func managerWithClock(t *testing.T, launch runtime.Launcher, clk timers.Clock, files map[string]string) *Manager {
	t.Helper()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m
}

func managerWithFake(t *testing.T, launch runtime.Launcher, files map[string]string) (*Manager, *timers.Fake) {
	t.Helper()
	fk := timers.NewFake(time.Time{})
	return managerWithClock(t, launch, fk.Clock(), files), fk
}

func waitCond(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		rt.Gosched()
	}
	t.Fatal("condition not met")
}

func waitSub(t *testing.T, m *Manager, name string, want core.Substate) {
	t.Helper()
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.subOfLocked(name) == want
	})
}

func waitState(t *testing.T, m *Manager, name string, want core.State) {
	t.Helper()
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked(name) == want
	})
}

func waitStarts(t *testing.T, launch *scriptedLauncher, n int, timeout time.Duration) {
	t.Helper()
	waitUntil(t, timeout, func() bool { return launch.nstarts() >= n })
}

func waitErr(t *testing.T, errc <-chan error) error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errc:
			return err
		default:
			rt.Gosched()
		}
	}
	t.Fatal("did not receive error result")
	return nil
}

func advanceWait(t *testing.T, fk *timers.Fake, d time.Duration) {
	t.Helper()
	waitCond(t, fk.Waiting)
	fk.Advance(d)
}

// advanceArmed waits until a fake NewTimer is due at or before now+d, then
// Advances d. advanceWait only checks that some wait is pending, so a leftover
// TimeoutStartSec / long wait can make it Advance before RestartSec is armed.
func advanceArmed(t *testing.T, fk *timers.Fake, d time.Duration) {
	t.Helper()
	waitCond(t, func() bool {
		when, ok := fk.NextWhen()
		return ok && !when.After(fk.Now().Add(d))
	})
	fk.Advance(d)
}
