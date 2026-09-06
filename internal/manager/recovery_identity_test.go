package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestLateRecoveryCannotAffectRecreatedUnit(t *testing.T) {
	for _, callback := range []string{"watchdog", "restart", "exit"} {
		t.Run(callback, func(t *testing.T) {
			const name = "work.service"
			body := "[Service]\nExecStart=C:\\Tools\\work.exe\n"
			m := managerWith(t, &fakeLauncher{}, map[string]string{name: body})
			if _, err := m.Start(context.Background(), name); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			oldRecord, oldGeneration := m.units[name], m.units[name].gen
			m.mu.Unlock()
			if _, err := m.Stop(name); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), name)); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Reload(); err != nil {
				t.Fatal(err)
			}
			writeUnit(t, m.cfg.UnitsDir(), name, body)
			if _, err := m.Reload(); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Start(context.Background(), name); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			current := m.units[name]
			proc := current.proc
			if current == oldRecord || current.gen != oldGeneration {
				m.mu.Unlock()
				t.Fatal("fixture did not recreate the same numeric generation")
			}
			m.mu.Unlock()
			if callback == "watchdog" {
				m.onWatchdogTimeout(runtimeIdentity{name: name, record: oldRecord, gen: oldGeneration})
			} else if callback == "restart" {
				m.beginRestart(recoveryRequest{owner: runtimeIdentity{name: name, record: oldRecord, gen: oldGeneration}})
			} else {
				m.applyProcessExit(processExitCompletion{owner: runtimeIdentity{name: name, record: oldRecord, gen: oldGeneration}, waitErr: errors.New("old invocation exited")})
			}
			m.mu.Lock()
			unchanged := m.units[name] == current && current.proc == proc && current.state == core.Active && current.sub != core.SubAutoRestart && !current.stopUncertain
			m.mu.Unlock()
			if !unchanged || !proc.Alive() {
				t.Fatal("late callback changed the recreated unit")
			}
		})
	}
}

type failSecondRecoveryLauncher struct {
	fakeLauncher
	calls atomic.Int32
}

func (l *failSecondRecoveryLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if l.calls.Add(1) == 2 {
		return nil, errors.New("injected fresh start failure")
	}
	return l.fakeLauncher.Start(ctx, spec)
}

func TestRecoveryRevalidatesAfterWaitingForUnitGate(t *testing.T) {
	const name = "work.service"
	launch := &failSecondRecoveryLauncher{}
	m := managerWith(t, launch, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	if _, err := m.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	rt := m.units[name]
	owner := runtimeIdentity{name: name, record: rt, gen: rt.gen}
	proc := rt.proc.(*fakeProc)
	m.mu.Unlock()
	unlock := m.ops.lock(name)
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	done := make(chan struct{})
	go func() { m.beginRestart(recoveryRequest{owner: owner}); close(done) }()
	waitCond(t, func() bool {
		m.ops.mu.Lock()
		defer m.ops.mu.Unlock()
		return m.ops.by[name].refs == 2
	})
	// The gate holder completes a newer explicit attempt while recovery is
	// already queued. Its failure must not authorize the old recovery request.
	proc.die(1)
	if err := m.launchUnitOp(context.Background(), name, false); err == nil {
		t.Fatal("fresh adapter start did not hit injected failure")
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("superseded recovery did not return")
	}
	if launch.calls.Load() != 2 {
		t.Fatal("old recovery launched after a newer failed start")
	}
}
