package manager

import (
	"context"
	"sync"
	"testing"
	"time"
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
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
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
