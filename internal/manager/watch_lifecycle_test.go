package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/pathwatch"
)

func TestReloadCoalescesBlockedWatchCleanup(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"watch.path":    "[Path]\nPathChanged=C:\\Data\\fixture\n",
		"watch.service": "[Service]\nExecStart=C:\\Tools\\worker.exe\n",
		"other.target":  "[Unit]\nDescription=Independent\n",
	})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	w := &controlledCloseWatch{release: release}
	w.fail.Store(true)
	m.pathOpen = func(pathwatch.Spec) (pathwatch.Watch, error) { return w, nil }
	if _, err := m.Start(context.Background(), "watch.path"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	h := m.units["watch.path"].hub
	m.mu.Unlock()
	if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "watch.path")); err != nil {
		t.Fatal(err)
	}
	workers := func() int {
		// Count this specific blocked worker class, not unrelated runtime or
		// test goroutines. Retain all stacks to avoid silently missing workers.
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		if n == len(buf) {
			t.Fatal("worker stack snapshot exceeded fixture bound")
		}
		count := 0
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(stack, "(*Manager).syncHubsLocked.func") {
				count++
			}
		}
		return count
	}
	baseline := workers()
	for i := 0; i < 32; i++ {
		if _, err := m.Reload(); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			waitCond(t, func() bool { return w.calls.Load() == 1 })
		}
		if n := workers() - baseline; n > 1 {
			t.Fatalf("one retained watch admitted %d reload cleanup workers", n)
		}
	}
	if _, err := m.Start(context.Background(), "other.target"); err != nil {
		t.Fatal("blocked watch cleanup prevented independent work", err)
	}
	if w.calls.Load() != 1 {
		t.Fatal("reload duplicated the native close")
	}
	unblock()
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !h.reloadClosing
	})
	status, err := m.Status("watch.path")
	if err != nil || !status.Unit.TerminationUncertain {
		t.Fatal("failed reload cleanup lost retained ownership", err)
	}
	w.fail.Store(false)
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["watch.path"].hub == nil && !h.reloadClosing
	})
	if w.calls.Load() != 2 {
		t.Fatal("later reload did not retry exactly one failed close")
	}
}

func TestLateWatchFailurePreservesStopDecision(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
	})
	h := &watchRuntime{}
	m.mu.Lock()
	rt := m.units["work.service"]
	h.gen = rt.gen
	rt.hub = h
	rt.state = core.Active
	rt.stopping = true // complete stop scope has been accepted
	rt.err = "retained diagnostic"
	m.mu.Unlock()
	m.failHub("work.service", h, errors.New("late watch failure"))
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt.state != core.Active || rt.err != "retained diagnostic" || rt.hub != h {
		t.Fatalf("late failure changed accepted stop: state=%v err=%q hub=%p", rt.state, rt.err, rt.hub)
	}
}

func TestWatchFailureCleanupDoesNotBlockIndependentStart(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
		"other.target": "[Unit]\nDescription=Independent\n",
	})
	release := make(chan struct{})
	defer close(release)
	w := &controlledCloseWatch{release: release}
	h := &watchRuntime{watches: []watchIO{w}}
	m.mu.Lock()
	rt := m.units["work.service"]
	h.gen = rt.gen
	rt.hub, rt.state = h, core.Active
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.failHub("work.service", h, errors.New("watch lost")); close(done) }()
	waitCond(t, func() bool { return w.calls.Load() > 0 })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := m.Start(ctx, "other.target"); err != nil {
		t.Fatal("independent start blocked by watch close", err)
	}
	m.mu.Lock()
	if rt.hub != h || !rt.cleanupPending() || rt.state != core.Failed {
		t.Error("pending close lost failed watch ownership")
	}
	m.mu.Unlock()
	select {
	case <-done:
		t.Fatal("blocked watch close returned early")
	default:
	}
}

func TestPathPredicateRejectsSupersededObservation(t *testing.T) {
	for _, action := range []string{"stop", "replacement", "generation", "unavailable", "close"} {
		t.Run(action, func(t *testing.T) {
			m := managerWith(t, &fakeLauncher{}, map[string]string{
				"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
			})
			h := &watchRuntime{}
			m.mu.Lock()
			rt := m.units["work.service"]
			h.gen = rt.gen
			rt.hub, rt.state = h, core.Active
			m.mu.Unlock()
			if !m.applyPathPredicate("work.service", h, true) || m.applyPathPredicate("work.service", h, true) {
				t.Fatal("predicate rising edge was not consumed exactly once")
			}
			m.mu.Lock()
			switch action {
			case "stop":
				rt.stopping = true
			case "replacement":
				rt.hub = &watchRuntime{gen: rt.gen}
			case "generation":
				rt.gen++
			case "unavailable":
				rt.unavailable = true
			case "close":
				m.closed = true
			}
			m.mu.Unlock()
			if m.applyPathPredicate("work.service", h, false) || !h.existsSatisfied {
				t.Fatal("superseded predicate changed the retained latch")
			}
			m.failHub("work.service", h, errors.New("late predicate failure"))
			m.mu.Lock()
			if rt.state != core.Active || rt.err != "" {
				t.Error("superseded failure changed lifecycle")
			}
			m.mu.Unlock()
		})
	}
}
