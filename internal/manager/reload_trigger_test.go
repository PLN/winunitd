package manager

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/pathwatch"
	"github.com/PLN/winunitd/internal/timers"
)

func TestLateWatchOpenCannotSurviveReloadOrClose(t *testing.T) {
	for _, action := range []string{"reload", "close"} {
		t.Run(action, func(t *testing.T) {
			hub := newFakePathHub()
			opened := make(chan *fakePathWatch, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			open := func(spec pathwatch.Spec) (pathwatch.Watch, error) {
				w, err := hub.Open(spec)
				if err != nil {
					return nil, err
				}
				opened <- w.(*fakePathWatch)
				<-release
				return w, nil
			}
			m := managerWithPath(t, &fakeLauncher{}, open, map[string]string{
				"worker.service": "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\n",
				"worker.path":    "[Path]\nPathChanged=C:\\Data\\incoming\n",
			})
			done := make(chan error, 1)
			go func() { _, err := m.Start(context.Background(), "worker.path"); done <- err }()
			var watch *fakePathWatch
			select {
			case watch = <-opened:
			case <-time.After(time.Second):
				t.Fatal("watch open not reached")
			}
			if action == "reload" {
				if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "worker.path")); err != nil {
					t.Fatal(err)
				}
				if _, err := m.Reload(); err != nil {
					t.Fatal(err)
				}
			} else {
				m.Close()
			}
			unblock()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("late watch installed after configuration loss/shutdown")
				}
			case <-time.After(time.Second):
				t.Fatal("late watch start did not finish")
			}
			watch.mu.Lock()
			closed := watch.closed
			watch.mu.Unlock()
			if !closed {
				t.Fatal("late watch handle leaked")
			}
		})
	}
}

func TestUnavailableRetainedTimerCannotArmOrFire(t *testing.T) {
	launch := &fakeLauncher{}
	m, _ := managerWithFake(t, launch, map[string]string{
		"worker.service": "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\n",
		"worker.timer":   "[Timer]\nOnStartupSec=10s\n",
	})
	if _, err := m.Start(context.Background(), "worker.timer"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	rt := m.units["worker.timer"]
	old := rt.unit
	// Model a lifecycle operation keeping the record through reload.
	rt.operations++
	m.mu.Unlock()
	defer func() { m.mu.Lock(); rt.operations--; m.mu.Unlock() }()
	if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "worker.timer")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if m.engine.Armed("worker.timer") {
		t.Fatal("reload kept unavailable timer armed")
	}
	if err := m.armTimer(old, ""); err == nil {
		t.Fatal("late arm accepted unavailable configuration")
	}
	// Also reject an elapsed callback even if an engine entry is stale.
	token := m.engine.Arm(timerSpec(old))
	m.onTimerElapsed(timers.Fire{Name: "worker.timer", Unit: old.Timer.Unit, Token: token})
	m.engine.Disarm("worker.timer")
	if len(launch.specs()) != 0 {
		t.Fatal("unavailable timer activated its companion")
	}
}
