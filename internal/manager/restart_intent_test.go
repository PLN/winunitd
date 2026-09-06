package manager

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestExplicitStopSupersedesRestartDuringCleanup(t *testing.T) {
	const name = "work.service"
	backend := newFakeSCM("example-worker")
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	adapter := &blockedSCMStop{SCM: backend, release: release}
	m := managerWithSCM(t, &fakeLauncher{}, adapter, map[string]string{
		name: "[Service]\nType=scm\nServiceName=example-worker\nTimeoutStopSec=30s\n",
	})
	if _, err := m.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	restartDone := make(chan error, 1)
	go func() { _, err := m.Restart(context.Background(), name); restartDone <- err }()
	waitCond(t, func() bool { return adapter.calls.Load() == 1 })
	stopDone := make(chan error, 1)
	go func() { _, err := m.Stop(name); stopDone <- err }()
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units[name].stopEpoch == 2
	})
	unblock()
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	restartErr := <-restartDone
	if got := backend.nstarts(); got != 1 {
		t.Fatalf("restart launched after later stop: %d starts", got)
	}
	if restartErr == nil {
		t.Fatal("superseded restart reported success")
	}
}

func TestRestartRestoresActivePartOfMembers(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"app.target":     "[Unit]\nDescription=Application\n",
		"worker.service": "[Unit]\nPartOf=app.target\nAfter=app.target\n[Service]\nExecStart=C:\\Tools\\worker.exe\n",
		"idle.service":   "[Unit]\nPartOf=app.target\n[Service]\nExecStart=C:\\Tools\\idle.exe\n",
	})
	for _, name := range []string{"app.target", "worker.service"} {
		if _, err := m.Start(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Restart(context.Background(), "app.target"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "worker.service", core.Active)
	assertState(t, m, "idle.service", core.Inactive)
}

func TestRestartKeepsExplicitStartLimitPolicy(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"work.service": "[Unit]\nStartLimitIntervalSec=1h\nStartLimitBurst=1\n[Service]\nExecStart=C:\\Tools\\work.exe\n",
	})
	for i := 0; i < 3; i++ {
		if _, err := m.Restart(context.Background(), "work"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStopInvalidatesRestartWaitingForDependency(t *testing.T) {
	testInvalidateRestartWaitingForDependency(t, false)
}

func TestRemovalInvalidatesRestartWaitingForDependency(t *testing.T) {
	testInvalidateRestartWaitingForDependency(t, true)
}

func testInvalidateRestartWaitingForDependency(t *testing.T, remove bool) {
	t.Helper()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"work.service": "[Unit]\nRequires=dep.service\nAfter=dep.service\n[Service]\nExecStart=C:\\Tools\\work.exe\n",
		"dep.service":  "[Service]\nExecStart=C:\\Tools\\dep.exe\n",
	})
	unlock := m.ops.lock("dep.service")
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	done := make(chan error, 1)
	go func() { _, err := m.Restart(context.Background(), "work"); done <- err }()
	waitCond(t, func() bool {
		m.ops.mu.Lock()
		defer m.ops.mu.Unlock()
		return m.ops.by["dep.service"].refs == 2
	})
	if remove {
		if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "work.service")); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Reload(); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := m.Stop("work"); err != nil {
			t.Fatal(err)
		}
	}
	release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("superseded dependency plan succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restart did not drain")
	}
	if len(launch.specs()) != 0 {
		t.Fatal("superseded restart launched a dependency")
	}
}

func TestRestartRejectsBeforeStoppingOnInvalidPlanOrCapacity(t *testing.T) {
	for _, reason := range []string{"plan", "capacity"} {
		t.Run(reason, func(t *testing.T) {
			m := managerWith(t, &fakeLauncher{}, map[string]string{
				"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
			})
			if _, err := m.Start(context.Background(), "work"); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			proc := m.units["work.service"].proc
			if reason == "capacity" {
				m.cfg.MaxStartTransactions = 1
				m.activeStarts = 1
			}
			m.mu.Unlock()
			defer func() { m.mu.Lock(); m.activeStarts = 0; m.mu.Unlock() }()
			if reason == "plan" {
				writeUnit(t, m.cfg.UnitsDir(), "work.service", "[Unit]\nRequires=missing.service\n[Service]\nExecStart=C:\\Tools\\work.exe\n")
				if _, err := m.Reload(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := m.Restart(context.Background(), "work"); err == nil {
				t.Fatal("invalid restart admitted")
			}
			if !proc.Alive() {
				t.Fatal("rejected restart stopped live invocation")
			}
		})
	}
}

func TestRestartCapturesConfigurationBeforeNativeStop(t *testing.T) {
	const name = "work.service"
	backend := newFakeSCM("example-old", "example-new")
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	adapter := &blockedSCMStop{SCM: backend, release: release}
	m := managerWithSCM(t, &fakeLauncher{}, adapter, map[string]string{
		name: "[Service]\nType=scm\nServiceName=example-old\nTimeoutStopSec=30s\n",
	})
	if _, err := m.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	revision := m.units[name].configRevision
	m.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := m.Restart(context.Background(), name); done <- err }()
	waitCond(t, func() bool { return adapter.calls.Load() == 1 })
	writeUnit(t, m.cfg.UnitsDir(), name, "[Service]\nType=scm\nServiceName=example-new\n")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	old, _ := backend.Query("example-old")
	newTarget, _ := backend.Query("example-new")
	if old.State != runtime.SCMRunning || newTarget.State != runtime.SCMStopped {
		t.Fatal("reload retargeted accepted restart")
	}
	m.mu.Lock()
	captured := m.units[name].invocationRevision
	m.mu.Unlock()
	if captured != revision {
		t.Fatal("restart lost its accepted revision")
	}
}
