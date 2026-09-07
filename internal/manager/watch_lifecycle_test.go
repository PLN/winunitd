package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
)

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
	if rt.hub != h || !rt.stopUncertain || rt.state != core.Failed {
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
