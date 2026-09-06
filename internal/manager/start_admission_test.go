package manager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
)

func TestStartCapacityRetainsTimerAndAllowsStop(t *testing.T) {
	launch := newGatedStartLauncher("blocked.service")
	var once sync.Once
	release := func() { once.Do(launch.release) }
	defer release()
	m, clock := managerWithFake(t, launch, map[string]string{
		"blocked.service": "[Service]\nExecStart=C:\\Tools\\blocked.exe\n",
		"work.service":    "[Service]\nExecStart=C:\\Tools\\work.exe\n",
		"work.timer":      "[Timer]\nOnStartupSec=1s\n",
		"stop.service":    "[Service]\nExecStart=C:\\Tools\\stop.exe\n",
	})
	m.cfg.MaxStartTransactions = 1
	for _, name := range []string{"work.timer", "stop.service"} {
		if _, err := m.Start(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "blocked.service"); done <- err }()
	select {
	case <-launch.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("start not admitted")
	}
	for i := 0; i < 50; i++ {
		if _, err := m.Start(context.Background(), "work.service"); !errors.Is(err, errStartCapacity) {
			t.Fatalf("overload result: %v", err)
		}
	}
	m.mu.Lock()
	retained := m.activeStarts == 1 && m.units["work.service"].operations == 0
	m.mu.Unlock()
	if !retained {
		t.Fatal("rejected starts allocated plans or released accepted work")
	}
	if _, err := m.Stop("stop.service"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "stop.service", core.Inactive)
	clock.Advance(2 * time.Second)
	m.engine.ClockChanged()
	waitCond(t, func() bool { s := m.engine.Status("work.timer"); return !s.Last.IsZero() && s.Next.After(clock.Now()) })
	assertState(t, m, "work.service", core.Inactive)
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admitted start did not finish")
	}
	clock.Advance(time.Second)
	m.engine.ClockChanged()
	waitState(t, m, "work.service", core.Active)
	if len(launch.specs()) != 3 {
		t.Fatal("timer activation was lost or duplicated after overload")
	}
	waitCond(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.activeStarts == 0 })
}

func TestStoppedWatchLeavesCapacityWait(t *testing.T) {
	launch := newGatedStartLauncher("blocked.service")
	var once sync.Once
	release := func() { once.Do(launch.release) }
	defer release()
	hub := newFakePathHub()
	m, _ := managerWithFake(t, launch, map[string]string{
		"blocked.service": "[Service]\nExecStart=C:\\Tools\\blocked.exe\n",
		"work.service":    "[Service]\nExecStart=C:\\Tools\\work.exe\n",
		"work.path":       "[Path]\nPathChanged=C:\\Data\\incoming\n",
	})
	m.cfg.MaxStartTransactions = 1
	m.pathOpen = hub.Open
	if _, err := m.Start(context.Background(), "work.path"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	source := m.units["work.path"].hub
	m.mu.Unlock()
	startDone := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "blocked.service"); startDone <- err }()
	select {
	case <-launch.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("start not admitted")
	}
	watchDone := make(chan struct{})
	go func() { m.onPathChanged("work.path", source); close(watchDone) }()
	waitCond(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.capacityWaiters == 1 })
	if _, err := m.Stop("work.path"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-watchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stopped watch retained its admission wait")
	}
	m.mu.Lock()
	valid := m.activeStarts == 1 && m.capacityWaiters == 0
	m.mu.Unlock()
	if !valid {
		t.Fatal("watch stop consumed or released another start's capacity")
	}
	release()
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("start did not finish")
	}
	if len(launch.specs()) != 1 {
		t.Fatal("stopped watch launched after capacity was released")
	}
}

func TestRejectedPlanReleasesStartCapacity(t *testing.T) {
	m, _ := managerWithFake(t, &fakeLauncher{}, map[string]string{
		"a.target":     "[Unit]\nRequires=missing.service\n",
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
	})
	m.cfg.MaxStartTransactions = 1
	for i := 0; i < 10; i++ {
		if _, err := m.Start(context.Background(), "a.target"); err == nil || errors.Is(err, errStartCapacity) {
			t.Fatalf("invalid plan admission: %v", err)
		}
	}
	if _, err := m.Start(context.Background(), "work.service"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	remaining := m.activeStarts
	m.mu.Unlock()
	if remaining != 0 {
		t.Fatal("completed/rejected plan retained start capacity")
	}
}
