package manager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
)

type heldProbeSCM struct {
	*fakeSCM
	hold     atomic.Bool
	calls    atomic.Int32
	release  chan struct{}
	err      error
	holdName string
}

func (s *heldProbeSCM) Query(name string) (runtime.SCMStatus, error) {
	if s.hold.Load() && (s.holdName == "" || s.holdName == name) {
		s.calls.Add(1)
		<-s.release
		return runtime.SCMStatus{State: runtime.SCMStopped}, s.err
	}
	return s.fakeSCM.Query(name)
}

func driveNativeUntil(t *testing.T, clock *timers.Fake, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("native observation did not converge")
		}
		clock.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNativeStopStopsBoundDependentWithRetainedPolicy(t *testing.T) {
	for _, kind := range []string{"scm", "scheduled-task"} {
		t.Run(kind, func(t *testing.T) {
			clock := timers.NewFake(time.Now())
			files := map[string]string{
				"peer.service":     "[Service]\nType=" + kind + "\nServiceName=original\n",
				"bound.service":    "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker + "Restart=always\n",
				"required.service": "[Unit]\nRequires=peer.service\nAfter=peer.service\n" + boundWorker,
			}
			var m *Manager
			var disappear func()
			if kind == "scm" {
				scm := newFakeSCM("original", "replacement")
				m = managerWithSCMClock(t, &fakeLauncher{}, scm, clock.Clock(), files)
				disappear = func() { scm.set("original", runtime.SCMStopped, 0) }
			} else {
				files["peer.service"] = strings.ReplaceAll(files["peer.service"], "ServiceName=", "TaskName=")
				tasks := newFakeTasks("original", "replacement")
				m = managerWithTasksClock(t, &fakeLauncher{}, tasks, clock.Clock(), files)
				disappear = func() { tasks.set("original", runtime.TaskReady, 0, 0) }
			}
			for _, name := range []string{"bound", "required"} {
				if _, err := m.Start(context.Background(), name); err != nil {
					t.Fatal(err)
				}
			}
			writeUnit(t, m.cfg.UnitsDir(), "peer.service", strings.ReplaceAll(files["peer.service"], "original", "replacement"))
			writeUnit(t, m.cfg.UnitsDir(), "bound.service", boundWorker)
			if _, err := m.Reload(); err != nil {
				t.Fatal(err)
			}
			disappear()
			driveNativeUntil(t, clock, func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				return m.units["peer.service"].state == core.Inactive && m.units["bound.service"].state == core.Inactive && m.boundStopsDone == nil
			})
			assertState(t, m, "required.service", core.Active)
			clock.Advance(10 * time.Second)
			assertState(t, m, "bound.service", core.Inactive)
		})
	}
}

func TestDelayedNativeProbeCannotStopReplacement(t *testing.T) {
	clock := timers.NewFake(time.Now())
	scm := &heldProbeSCM{fakeSCM: newFakeSCM("original"), release: make(chan struct{})}
	scm.hold.Store(true)
	m := managerWithSCMClock(t, &fakeLauncher{}, scm, clock.Clock(), map[string]string{
		"peer.service":  "[Service]\nType=scm\nServiceName=original\n",
		"bound.service": "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker,
	})
	t.Cleanup(func() {
		scm.hold.Store(false)
		select {
		case <-scm.release:
		default:
			close(scm.release)
		}
	})
	if _, err := m.Start(context.Background(), "bound"); err != nil {
		t.Fatal(err)
	}
	driveNativeUntil(t, clock, func() bool { return scm.calls.Load() == 1 })
	if _, err := m.Stop("peer"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "peer"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "bound"); err != nil {
		t.Fatal(err)
	}
	scm.hold.Store(false)
	close(scm.release)
	waitCond(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return len(m.nativeProbes) == 0 })
	assertState(t, m, "peer.service", core.Active)
	assertState(t, m, "bound.service", core.Active)
}

func TestNativeProbeCapacityAndCloseRetainBlockedQueries(t *testing.T) {
	clock := timers.NewFake(time.Now())
	scm := &heldProbeSCM{fakeSCM: newFakeSCM("original"), release: make(chan struct{})}
	scm.hold.Store(true)
	files := make(map[string]string)
	for i := 0; i < maxNativeProbes+2; i++ {
		files[fmt.Sprintf("proxy%d.service", i)] = "[Service]\nType=scm\nServiceName=original\n"
	}
	m := managerWithSCMClock(t, &fakeLauncher{}, scm, clock.Clock(), files)
	t.Cleanup(func() {
		select {
		case <-scm.release:
		default:
			close(scm.release)
		}
	})
	for name := range files {
		if _, err := m.Start(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	driveNativeUntil(t, clock, func() bool { return scm.calls.Load() == maxNativeProbes })
	clock.Advance(time.Hour)
	snapshot, err := m.Snapshot()
	if err != nil || snapshot.Machine.NativeProbes != maxNativeProbes || scm.calls.Load() != maxNativeProbes {
		t.Fatalf("unbounded queries: %+v %v", snapshot, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.CloseContext(ctx); err == nil {
		t.Fatal("close abandoned native queries")
	}
	close(scm.release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := m.CloseContext(ctx2); err != nil {
		t.Fatal(err)
	}
	if scm.calls.Load() != maxNativeProbes {
		t.Fatal("closed manager admitted another query")
	}
}

func TestNativeProbeErrorIsUnknownAndBounded(t *testing.T) {
	clock := timers.NewFake(time.Now())
	scm := &heldProbeSCM{fakeSCM: newFakeSCM("original"), release: make(chan struct{}), err: errors.New(strings.Repeat("unavailable ", 1000))}
	scm.hold.Store(true)
	m := managerWithSCMClock(t, &fakeLauncher{}, scm, clock.Clock(), map[string]string{
		"peer.service":  "[Service]\nType=scm\nServiceName=original\n",
		"bound.service": "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker,
	})
	t.Cleanup(func() {
		select {
		case <-scm.release:
		default:
			close(scm.release)
		}
	})
	if _, err := m.Start(context.Background(), "bound"); err != nil {
		t.Fatal(err)
	}
	driveNativeUntil(t, clock, func() bool { return scm.calls.Load() == 1 })
	close(scm.release)
	waitCond(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.units["peer.service"].nativeProbeError != "" })
	snapshot, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := snapshotUnit(t, snapshot, "peer.service").NativeObservationError
	if len(got) > nativeProbeErrorLimit || !strings.HasSuffix(got, "...") {
		t.Fatalf("unbounded probe error length %d", len(got))
	}
	assertState(t, m, "peer.service", core.Active)
	assertState(t, m, "bound.service", core.Active)
	scm.hold.Store(false)
	driveNativeUntil(t, clock, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.units["peer.service"].nativeProbeError == "" })
}

func TestBlockedNativeProbeLeavesOtherProxiesProgressing(t *testing.T) {
	clock := timers.NewFake(time.Now())
	scm := &heldProbeSCM{fakeSCM: newFakeSCM("blocked", "moving"), release: make(chan struct{}), holdName: "blocked"}
	scm.hold.Store(true)
	m := managerWithSCMClock(t, &fakeLauncher{}, scm, clock.Clock(), map[string]string{
		"blocked.service": "[Service]\nType=scm\nServiceName=blocked\n",
		"moving.service":  "[Service]\nType=scm\nServiceName=moving\n",
		"bound.service":   "[Unit]\nBindsTo=moving.service\nAfter=moving.service\n" + boundWorker,
	})
	t.Cleanup(func() { close(scm.release) })
	for _, name := range []string{"blocked", "bound"} {
		if _, err := m.Start(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	driveNativeUntil(t, clock, func() bool { return scm.calls.Load() == 1 })
	scm.set("moving", runtime.SCMStopped, 0)
	driveNativeUntil(t, clock, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["bound.service"].state == core.Inactive && m.boundStopsDone == nil
	})
	if scm.calls.Load() != 1 {
		t.Fatal("blocked observation was duplicated")
	}
}

func TestNativeTaskInstancesPreventFalseDisappearance(t *testing.T) {
	clock := timers.NewFake(time.Now())
	tasks := newFakeTasks("original")
	m := managerWithTasksClock(t, &fakeLauncher{}, tasks, clock.Clock(), map[string]string{
		"peer.service":  "[Service]\nType=scheduled-task\nTaskName=original\n",
		"bound.service": "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker,
	})
	if _, err := m.Start(context.Background(), "bound"); err != nil {
		t.Fatal(err)
	}
	tasks.set("original", runtime.TaskReady, 1, 42)
	driveNativeUntil(t, clock, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.units["peer.service"].nativeNextProbe.IsZero() && len(m.nativeProbes) == 0
	})
	assertState(t, m, "peer.service", core.Active)
	assertState(t, m, "bound.service", core.Active)
	tasks.set("original", runtime.TaskDisabled, 0, 0)
	driveNativeUntil(t, clock, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["bound.service"].state == core.Inactive && m.boundStopsDone == nil
	})
}
