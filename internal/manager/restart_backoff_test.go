package manager

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

const backoffUnit = "[Unit]\nFormatVersion=2\nStartLimitBurst=0\n[Service]\nExecStart=C:\\Apps\\work.exe\nRestart=on-failure\nRestartSec=1s\nRestartBackoff=exponential\nRestartMaxDelaySec=3s\n"

func TestRestartBackoffPublicLifecycle(t *testing.T) {
	launch := &fakeLauncher{}
	m, clock := managerWithFake(t, launch, map[string]string{"work.service": backoffUnit})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	for i, delay := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second} {
		m.mu.Lock()
		rt := m.units["work.service"]
		old := rt.proc.(*fakeProc)
		owner := runtimeIdentity{name: "work.service", record: rt, gen: rt.gen}
		m.mu.Unlock()
		old.die(7)
		waitCond(t, func() bool { return clock.WaitingAt(delay) })
		status, err := m.Status("work")
		if err != nil || status.Unit.RestartAttempt != uint32(i+1) || status.Unit.RestartDelaySec != delay.Seconds() {
			t.Fatalf("backoff status: %+v, %v", status, err)
		}
		snapshot, err := m.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		view := snapshotUnit(t, snapshot, "work.service")
		if view.RestartAttempt != status.Unit.RestartAttempt || view.RestartDelaySec != status.Unit.RestartDelaySec {
			t.Fatal("snapshot backoff differs")
		}
		// Re-delivery cannot increase the attempt or replace its accepted timer.
		if m.acceptRecovery(recoveryRequest{owner: owner}) != nil {
			t.Fatal("duplicate recovery accepted")
		}
		if i == 1 {
			writeUnit(t, m.cfg.UnitsDir(), "work.service", "[Service]\nExecStart=C:\\Apps\\replacement.exe\nRestart=on-failure\nRestartSec=30s\n")
			if result, err := m.Reload(); err != nil || len(result.Errors) != 0 {
				t.Fatalf("reload: %+v %v", result, err)
			}
		}
		clock.Advance(delay)
		waitCond(t, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return rt.proc != nil && rt.proc != old && rt.state == core.Active
		})
		if view.RestartAttempt != uint32(i+1) {
			t.Fatal("old snapshot changed")
		}
	}
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status("work")
	if err != nil || status.Unit.RestartAttempt != 0 || status.Unit.RestartDelaySec != 0 {
		t.Fatalf("explicit launch did not reset backoff: %+v %v", status, err)
	}
	m.mu.Lock()
	current := m.units["work.service"].proc.(*fakeProc)
	m.mu.Unlock()
	current.die(7)
	waitCond(t, func() bool { return clock.WaitingAt(30 * time.Second) })
	if specs := launch.specs(); specs[len(specs)-1].Argv[0] != "C:\\Apps\\replacement.exe" {
		t.Fatal("explicit launch did not adopt replacement")
	}
}

func TestRestartBackoffStopAndRemovalCancelAcceptedWait(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "remove"}[remove], func(t *testing.T) {
			launch := &fakeLauncher{}
			m, clock := managerWithFake(t, launch, map[string]string{"work.service": backoffUnit})
			if _, err := m.Start(context.Background(), "work"); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			p := m.units["work.service"].proc.(*fakeProc)
			m.mu.Unlock()
			p.die(7)
			waitCond(t, func() bool { return clock.WaitingAt(time.Second) })
			m.mu.Lock()
			rt := m.units["work.service"]
			owner := runtimeIdentity{name: "work.service", record: rt, gen: rt.gen}
			m.mu.Unlock()
			if remove {
				if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "work.service")); err != nil {
					t.Fatal(err)
				}
				if _, err := m.Reload(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := m.Stop("work"); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			canceled := rt.restartCancel == nil && rt.restartDelay == 0
			m.mu.Unlock()
			if !canceled || m.acceptRecovery(recoveryRequest{owner: owner}) != nil {
				t.Fatal("stop/removal did not disarm recovery")
			}
			clock.Advance(time.Hour)
			if len(launch.specs()) != 1 {
				t.Fatal("superseded recovery launched")
			}
		})
	}
}

func TestRestartBackoffDurationSaturates(t *testing.T) {
	if got := cappedRestartDelay(time.Nanosecond, time.Duration(math.MaxInt64-1), math.MaxUint32); got != time.Duration(math.MaxInt64-1) {
		t.Fatal("large exponent overflowed", got)
	}
	if got := cappedRestartDelay(1<<62, time.Duration(math.MaxInt64-1), 2); got != time.Duration(math.MaxInt64-1) {
		t.Fatal("large base overflowed", got)
	}
}

type backoffFailedLauncher struct{ attempts atomic.Int32 }

func (l *backoffFailedLauncher) Start(context.Context, runtime.StartSpec) (runtime.Process, error) {
	l.attempts.Add(1)
	return nil, &runtime.ExitStatus{Code: 7}
}

func TestRestartBackoffStartFailuresRespectStartLimit(t *testing.T) {
	launch := &backoffFailedLauncher{}
	body := strings.Replace(backoffUnit, "StartLimitBurst=0", "StartLimitBurst=3\nStartLimitIntervalSec=60s", 1)
	m, clock := managerWithFake(t, launch, map[string]string{"work.service": body})
	if _, err := m.Start(context.Background(), "work"); err == nil {
		t.Fatal("launch failure reported success")
	}
	for _, delay := range []time.Duration{time.Second, 2 * time.Second} {
		waitCond(t, func() bool { return clock.WaitingAt(delay) })
		clock.Advance(delay)
	}
	waitCond(t, func() bool {
		status, err := m.Status("work")
		return err == nil && status.Unit.Reason == core.ReasonStartLimit
	})
	status, err := m.Status("work")
	if err != nil || launch.attempts.Load() != 3 || status.Unit.RestartAttempt != 2 || status.Unit.RestartDelaySec != 0 || status.Unit.MainPID != 0 {
		t.Fatalf("start limit lost: %+v %v attempts=%d", status, err, launch.attempts.Load())
	}
}
