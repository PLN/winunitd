package manager

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/pathwatch"
)

func TestReloadKeepsArmedExistenceConditions(t *testing.T) {
	launch := &fakeLauncher{}
	hub := newFakePathHub()
	m := managerWithExists(t, launch, hub, map[string]string{
		"input.path":    "[Path]\nPathExists=C:\\Data\\old.flag\n",
		"input.service": "[Service]\nExecStart=C:\\Tools\\input.exe\n",
	})
	if _, err := m.Start(context.Background(), "input.path"); err != nil {
		t.Fatal(err)
	}
	before, _ := m.Status("input.path")
	armedRevision := before.Unit.ArmedConfigRevision
	if armedRevision == "" || armedRevision != before.Unit.ConfigRevision {
		t.Fatal("watch did not capture the accepted revision")
	}
	writeUnit(t, m.cfg.UnitsDir(), "input.path", "[Path]\nPathExists=C:\\Data\\new.flag\n")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	after, _ := m.Status("input.path")
	if after.Unit.ArmedConfigRevision != armedRevision || after.Unit.ConfigRevision == armedRevision {
		t.Fatal("reload relabelled the armed watch")
	}
	hub.mu.Lock()
	hub.exists[`C:\Data\old.flag`] = true
	hub.mu.Unlock()
	m.onPathExistsMaybe("input.path")
	if len(launch.specs()) != 1 {
		t.Fatal("armed watch used reloaded existence conditions")
	}
	if _, err := m.Stop("input.path"); err != nil {
		t.Fatal(err)
	}
	stopped, _ := m.Status("input.path")
	if stopped.Unit.ArmedConfigRevision != "" {
		t.Fatal("closed watch retained an armed revision")
	}
	if _, err := m.Stop("input.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "input.path"); err != nil {
		t.Fatal(err)
	}
	rearmed, _ := m.Status("input.path")
	if rearmed.Unit.ArmedConfigRevision != rearmed.Unit.ConfigRevision || rearmed.Unit.ArmedConfigRevision == armedRevision {
		t.Fatal("fresh watch did not capture the accepted revision")
	}
	hub.mu.Lock()
	hub.exists[`C:\Data\new.flag`] = true
	hub.mu.Unlock()
	m.onPathExistsMaybe("input.path")
	if len(launch.specs()) != 2 {
		t.Fatal("fresh watch did not adopt reloaded conditions")
	}
}

func TestDelayedWatchOpenRetainsConfigurationRevision(t *testing.T) {
	hub := newFakePathHub()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	m := managerWithPath(t, &fakeLauncher{}, func(spec pathwatch.Spec) (pathwatch.Watch, error) {
		close(entered)
		<-release
		return hub.Open(spec)
	}, map[string]string{
		"work.path":    "[Path]\nPathChanged=C:\\Data\\old.flag\n",
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
	})
	before, _ := m.Status("work.path")
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "work.path"); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("watch start returned before open: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not reach delayed open")
	}
	writeUnit(t, m.cfg.UnitsDir(), "work.path", "[Path]\nPathChanged=C:\\Data\\new.flag\n")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch start did not finish")
	}
	after, _ := m.Status("work.path")
	if after.Unit.ArmedConfigRevision != before.Unit.ConfigRevision || after.Unit.ArmedConfigRevision == after.Unit.ConfigRevision {
		t.Fatal("delayed watch was labelled with a definition it did not open")
	}
}

func TestOldExistenceProbeCannotAffectReplacementWatch(t *testing.T) {
	launch := &fakeLauncher{}
	hub := newFakePathHub()
	entered, released := make(chan struct{}), make(chan struct{})
	var enabled atomic.Bool
	var probes atomic.Int32
	var once sync.Once
	release := func() { once.Do(func() { close(released) }) }
	m := managerWithPathCfg(t, Config{
		Launch: launch, PathExistsOpen: hub.OpenExists,
		PathExists: func(pathwatch.Spec) (bool, error) {
			if enabled.Load() && probes.Add(1) == 1 {
				close(entered)
				<-released
				return true, nil
			}
			return false, nil
		},
	}, map[string]string{
		"input.path":    "[Path]\nPathExists=C:\\Data\\ready.flag\n",
		"input.service": "[Service]\nExecStart=C:\\Tools\\input.exe\n",
	})
	defer release()
	if _, err := m.Start(context.Background(), "input.path"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	old := m.units["input.path"].hub
	m.mu.Unlock()
	enabled.Store(true)
	done := make(chan struct{})
	go func() { m.onPathExistsForHub("input.path", old); close(done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not block")
	}
	if _, err := m.Stop("input.path"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "input.path"); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("old probe did not finish")
	}
	m.failHub("input.path", old, errors.New("late failure from old watch"))
	assertState(t, m, "input.path", core.Active)
	m.mu.Lock()
	fresh := m.units["input.path"].hub
	satisfied := fresh != nil && fresh.existsSatisfied
	m.mu.Unlock()
	if fresh == nil || fresh == old || satisfied || len(launch.specs()) != 0 {
		t.Fatal("old probe/failure affected replacement watch")
	}
}

func TestStoppedWatchCannotLaunchQueuedCompanion(t *testing.T) {
	launch := &fakeLauncher{}
	hub := newFakePathHub()
	m := managerWithPath(t, launch, hub.Open, map[string]string{
		"input.path":    "[Path]\nPathChanged=C:\\Data\\incoming\n",
		"input.service": "[Service]\nExecStart=C:\\Tools\\input.exe\n",
	})
	if _, err := m.Start(context.Background(), "input.path"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	source := m.units["input.path"].hub
	m.mu.Unlock()
	unlock := m.ops.lock("input.service")
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	done := make(chan struct{})
	go func() { m.onPathChanged("input.path", source); close(done) }()
	waitCond(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.units["input.service"].operations > 0 })
	if _, err := m.Stop("input.path"); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("queued callback did not finish")
	}
	if len(launch.specs()) != 0 {
		t.Fatal("stopped source launched a queued companion")
	}
	assertState(t, m, "input.service", core.Inactive)
}

func TestStoppedWatchPreservesAlreadyLaunchedCompanion(t *testing.T) {
	launch := newGatedStartLauncher("input.service")
	hub := newFakePathHub()
	m := managerWithPath(t, launch, hub.Open, map[string]string{
		"input.path":    "[Path]\nPathChanged=C:\\Data\\incoming\n",
		"input.service": "[Service]\nExecStart=C:\\Tools\\input.exe\n",
	})
	var once sync.Once
	release := func() { once.Do(launch.release) }
	defer release()
	if _, err := m.Start(context.Background(), "input.path"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	source := m.units["input.path"].hub
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.onPathChanged("input.path", source); close(done) }()
	select {
	case <-launch.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("companion did not reach launcher")
	}
	if _, err := m.Stop("input.path"); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("companion launch did not finish")
	}
	assertState(t, m, "input.service", core.Active)
	if len(launch.specs()) != 1 {
		t.Fatal("admitted companion was not launched")
	}
	if _, err := m.Stop("input.service"); err != nil {
		t.Fatal(err)
	}
}

func TestReloadPreservesWatchBeforeStartPublication(t *testing.T) {
	launch := &fakeLauncher{}
	hub := newFakePathHub()
	m := managerWithPath(t, launch, hub.Open, map[string]string{
		"input.path":    "[Path]\nPathChanged=C:\\Data\\incoming\n",
		"input.service": "[Service]\nExecStart=C:\\Tools\\input.exe\n",
	})
	// Drive adapter completion separately from the start transaction's publication.
	// The transaction still retains the record in this real unlocked interval.
	m.mu.Lock()
	rt := m.units["input.path"]
	rt.operations++
	m.mu.Unlock()
	defer func() { m.mu.Lock(); rt.operations--; m.mu.Unlock() }()
	if err := m.launchUnit(context.Background(), "input.path", false); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	original := rt.hub
	m.mu.Unlock()
	if original == nil {
		t.Fatal("adapter did not install watch")
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	retained := rt.hub == original && !rt.stopUncertain
	m.mu.Unlock()
	if !retained {
		t.Fatal("valid reload disposed a watch before start publication")
	}
	if _, err := m.Stop("input.path"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	remaining := rt.hub
	m.mu.Unlock()
	if remaining != nil {
		t.Fatal("explicit stop retained watch")
	}
}
