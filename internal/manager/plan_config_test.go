package manager

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestStartPlanRetainsDefinitionsAcrossReload(t *testing.T) {
	launch := newGatedStartLauncher("first.service")
	m := managerWith(t, launch, map[string]string{
		"app.target":    "[Unit]\nRequires=first.service later.service\nAfter=first.service later.service\n",
		"first.service": "[Service]\nExecStart=C:\\Tools\\first.exe\n",
		"later.service": "[Unit]\nAfter=first.service\n[Service]\nExecStart=C:\\Tools\\old.exe\n",
	})
	initial, err := m.Status("later.service")
	if err != nil || initial.Unit.ConfigRevision == "" {
		t.Fatal("missing accepted revision")
	}
	acceptedRevision := initial.Unit.ConfigRevision
	var once sync.Once
	release := func() { once.Do(launch.release) }
	defer release()
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "app.target"); done <- err }()
	select {
	case <-launch.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("first dependency did not reach launcher")
	}
	writeUnit(t, m.cfg.UnitsDir(), "later.service", "[Service]\nExecStart=C:\\Tools\\new.exe\n")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("start transaction did not finish")
	}
	specs := launch.specs()
	if len(specs) != 2 || specs[1].Argv[0] != `C:\Tools\old.exe` {
		t.Fatal("accepted graph used reloaded command")
	}
	status, err := m.Status("later.service")
	if err != nil || status.Unit.ConfigRevision == acceptedRevision || status.Unit.InvocationConfigRevision != acceptedRevision {
		t.Fatal("queued plan did not retain its captured revision identity")
	}
	if _, err := m.Stop("later"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "later"); err != nil {
		t.Fatal(err)
	}
	specs = launch.specs()
	if len(specs) != 3 || specs[2].Argv[0] != `C:\Tools\new.exe` {
		t.Fatal("new plan did not use new command")
	}
	status, err = m.Status("later.service")
	if err != nil || status.Unit.InvocationConfigRevision != status.Unit.ConfigRevision {
		t.Fatal("new plan did not capture the current revision")
	}
}

func TestStopCancelsStartWaitingForDependency(t *testing.T) {
	launch := newGatedStartLauncher("first.service")
	m := managerWith(t, launch, map[string]string{
		"later.service": "[Unit]\nRequires=first.service\nAfter=first.service\n[Service]\nExecStart=C:\\Tools\\later.exe\n",
		"first.service": "[Service]\nExecStart=C:\\Tools\\first.exe\n",
	})
	var once sync.Once
	release := func() { once.Do(launch.release) }
	defer release()
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "later"); done <- err }()
	select {
	case <-launch.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("first dependency did not reach launcher")
	}
	if _, err := m.Stop("later"); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("superseded plan reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("start transaction did not finish")
	}
	if specs := launch.specs(); len(specs) != 1 || specs[0].Unit != "first.service" {
		t.Fatal("stopped pending member was launched")
	}
	status, err := m.Status("later")
	if err != nil || status.Unit.ActiveState != "inactive" || status.Unit.Error != "" {
		t.Fatal("old transaction overwrote completed stop")
	}
	if _, err := m.Start(context.Background(), "later"); err != nil {
		t.Fatal(err)
	}
	if len(launch.specs()) != 2 {
		t.Fatal("fresh start after stop did not launch")
	}
}

func TestStartTransactionDoesNotReviveExitedMember(t *testing.T) {
	launch := newGatedStartLauncher("slow.service")
	m := managerWith(t, launch, map[string]string{
		"app.target":    "[Unit]\nRequires=first.service slow.service\nAfter=first.service slow.service\n",
		"first.service": "[Service]\nExecStart=C:\\Tools\\first.exe\n",
		"slow.service":  "[Unit]\nAfter=first.service\n[Service]\nExecStart=C:\\Tools\\slow.exe\n",
	})
	var once sync.Once
	release := func() { once.Do(launch.release) }
	defer release()
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "app.target"); done <- err }()
	select {
	case <-launch.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("slow dependency did not reach launcher")
	}
	m.mu.Lock()
	first := m.units["first.service"].proc.(*fakeProc)
	m.mu.Unlock()
	first.die(1)
	waitState(t, m, "first.service", core.Failed)
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("start transaction did not finish")
	}
	assertState(t, m, "first.service", core.Failed)
}

type transactionStopLauncher struct {
	fakeLauncher
	fail    atomic.Bool
	slow    *blockedStopProcess
	release chan struct{}
}

func (l *transactionStopLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if spec.Unit == "worker.service" && l.fail.Load() {
		return nil, errors.New("injected new start failure")
	}
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil || spec.Unit != "slow.service" {
		return p, err
	}
	l.slow = &blockedStopProcess{Process: p, release: l.release}
	return l.slow, nil
}

func TestStopTransactionDoesNotOverwriteLaterFailedStart(t *testing.T) {
	launch := &transactionStopLauncher{release: make(chan struct{})}
	m := managerWith(t, launch, map[string]string{
		"app.target":     "[Unit]\nWants=worker.service slow.service\n",
		"worker.service": "[Unit]\nPartOf=app.target\nAfter=slow.service\n[Service]\nExecStart=C:\\Tools\\worker.exe\n",
		"slow.service":   "[Unit]\nPartOf=app.target\n[Service]\nExecStart=C:\\Tools\\slow.exe\n",
	})
	var once sync.Once
	release := func() { once.Do(func() { close(launch.release) }) }
	defer release()
	if _, err := m.Start(context.Background(), "app.target"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.Stop("app.target"); done <- err }()
	waitCond(t, func() bool { return launch.slow.calls.Load() == 1 })
	assertState(t, m, "worker.service", core.Inactive)
	launch.fail.Store(true)
	if _, err := m.Start(context.Background(), "worker"); err == nil {
		t.Fatal("new start failure missing")
	}
	assertState(t, m, "worker.service", core.Failed)
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop transaction did not finish")
	}
	assertState(t, m, "worker.service", core.Failed)
}
