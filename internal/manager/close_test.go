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
	"github.com/PLN/winunitd/internal/pathwatch"
)

type controlledCloseWatch struct {
	calls   atomic.Int32
	fail    atomic.Bool
	release <-chan struct{}
}

func TestWatchStopFailureRetainsReloadRouting(t *testing.T) {
	const name = "cleanup.path"
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		name[:len(name)-len(".path")] + ".service": "[Service]\nExecStart=C:\\Tools\\worker.exe\n",
		name: "[Path]\nPathChanged=C:\\Data\\incoming\n"})
	w := &controlledCloseWatch{}
	w.fail.Store(true)
	m.mu.Lock()
	if m.units[name] == nil {
		m.mu.Unlock()
		t.Fatal("fixture unit not loaded")
	}
	m.units[name].hub = &watchRuntime{watches: []watchIO{w}}
	m.units[name].state = core.Active
	m.mu.Unlock()
	if _, err := m.stopUnit(name); err == nil {
		t.Fatal("failed watch stop reported success")
	}
	if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), name)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	rt := m.units[name]
	retained := rt != nil && rt.hub != nil && rt.stopUncertain && rt.unavailable
	m.mu.Unlock()
	if !retained {
		t.Fatal("reload lost failed watch stop routing")
	}
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("start admitted unresolved cleanup")
	}
	w.fail.Store(false)
	if _, err := m.stopUnit(name); err != nil {
		// Reload may already own a close that observed the injected failure.
		// Joining that attempt reports its result; the next retry is fresh.
		if _, err := m.stopUnit(name); err != nil {
			t.Fatal("fresh stop retry", err)
		}
	}
	m.mu.Lock()
	retained = m.units[name].hub != nil || m.units[name].stopUncertain
	m.mu.Unlock()
	if retained {
		t.Fatal("successful retry retained watch ownership")
	}
}

func TestWatchStopDeadlineJoinsPendingClose(t *testing.T) {
	const name = "pending.path"
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		name[:len(name)-len(".path")] + ".service": "[Service]\nExecStart=C:\\Tools\\worker.exe\n",
		name: "[Path]\nPathChanged=C:\\Data\\incoming\n"})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	w := &controlledCloseWatch{release: release}
	m.mu.Lock()
	if m.units[name] == nil {
		m.mu.Unlock()
		t.Fatal("fixture unit not loaded")
	}
	m.units[name].hub = &watchRuntime{watches: []watchIO{w}}
	m.units[name].state = core.Active
	m.mu.Unlock()
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := m.stopUnitWithContext(ctx, name)
		cancel()
		if err == nil {
			t.Fatal("blocked watch stop reported success")
		}
	}
	if w.calls.Load() != 1 {
		t.Fatal("retry duplicated pending native close")
	}
	unblock()
	if _, err := m.stopUnit(name); err != nil {
		t.Fatal("stop retry", err)
	}
	if w.calls.Load() != 1 {
		t.Fatal("completed watch closed again")
	}
}

func TestPartialWatchOpenRetainsFailedCleanup(t *testing.T) {
	const name = "partial.path"
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		name[:len(name)-len(".path")] + ".service": "[Service]\nExecStart=C:\\Tools\\worker.exe\n",
		name: "[Path]\nPathChanged=C:\\Data\\first\nPathChanged=C:\\Data\\second\n"})
	w := &controlledCloseWatch{}
	w.fail.Store(true)
	var opens int
	m.pathOpen = func(pathwatch.Spec) (pathwatch.Watch, error) {
		opens++
		if opens == 1 {
			return w, nil
		}
		return nil, errors.New("injected open failure")
	}
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("partial open reported success")
	}
	m.mu.Lock()
	retained := m.units[name].hub != nil && m.units[name].stopUncertain
	m.mu.Unlock()
	if !retained {
		t.Fatal("partial open discarded failed cleanup")
	}
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("partial cleanup allowed replacement")
	}
	w.fail.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	if w.calls.Load() != 2 {
		t.Fatal("manager close did not retry partial open cleanup")
	}
}

func (w *controlledCloseWatch) C() <-chan struct{} { return nil }
func (w *controlledCloseWatch) Close() error {
	w.calls.Add(1)
	if w.release != nil {
		<-w.release
	}
	if w.fail.Load() {
		return errors.New("injected watch close failure")
	}
	return nil
}

func TestManagerCloseDeadlineJoinsPendingWatch(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	w := &controlledCloseWatch{release: release}
	m := &Manager{units: map[string]*unitRuntime{"pending.path": {hub: &watchRuntime{watches: []watchIO{w}}}}}
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := m.CloseContext(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("close result: %v", err)
		}
	}
	if w.calls.Load() != 1 {
		t.Fatal("close retry duplicated a blocked watcher close")
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if !closed {
		t.Fatal("deadline reopened manager admission")
	}
	unblock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal("close retry", err)
	}
	if w.calls.Load() != 1 {
		t.Fatal("completed watch was closed again")
	}
}

func TestManagerCloseRetainsFailedWatch(t *testing.T) {
	w := &controlledCloseWatch{}
	w.fail.Store(true)
	m := &Manager{units: map[string]*unitRuntime{"failed.path": {hub: &watchRuntime{watches: []watchIO{w}}}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err == nil {
		t.Fatal("failed watch close reported success")
	}
	m.mu.Lock()
	pending := len(m.closePending)
	m.mu.Unlock()
	if pending != 1 {
		t.Fatal("failed watch close lost ownership")
	}
	w.fail.Store(false)
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal("close retry", err)
	}
	m.mu.Lock()
	pending = len(m.closePending)
	m.mu.Unlock()
	if pending != 0 || w.calls.Load() != 2 {
		t.Fatal("failed close retry did not finish")
	}
}

func TestShutdownDeadlineJoinsBlockedNotificationClose(t *testing.T) {
	const name = "notify-close.service"
	m := managerWith(t, &fakeLauncher{}, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\worker.exe\nTimeoutStopSec=30s\n"})
	if _, err := m.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	nrt := &notifyRuntime{cancel: func() {}, done: make(chan struct{}), serveDone: release}
	m.mu.Lock()
	m.units[name].notify = nrt
	proc := m.units[name].proc
	m.mu.Unlock()
	var first *stopAttempt
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := m.Shutdown(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown result: %v", err)
		}
		m.stops.mu.Lock()
		pending := m.stops.pending[stopKey{shutdown: m}]
		m.stops.mu.Unlock()
		if pending == nil {
			t.Fatal("deadline discarded pending shutdown")
		}
		if first == nil {
			first = pending
		} else if pending != first {
			t.Fatal("retry duplicated blocked shutdown")
		}
	}
	m.mu.Lock()
	retained := m.units[name].proc == proc && m.units[name].stopUncertain
	m.mu.Unlock()
	if !retained {
		t.Fatal("pending control close lost process ownership")
	}
	unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal("shutdown retry inherited expired context", err)
	}
	if proc.Alive() {
		t.Fatal("shutdown retry left process alive")
	}
}
