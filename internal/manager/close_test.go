package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/pathwatch"
)

type controlledCloseWatch struct {
	calls   atomic.Int32
	fail    atomic.Bool
	release <-chan struct{}
}

type controlledNotifyListener struct{ controlledCloseWatch }

func (l *controlledNotifyListener) Addr() string { return "test" }
func (l *controlledNotifyListener) Accept() (notify.Conn, error) {
	return nil, errors.New("test listener does not accept")
}

func TestNotificationCloseFailureRetainsStopOwnership(t *testing.T) {
	const name = "notification-close.service"
	m := managerWith(t, &fakeLauncher{}, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\worker.exe\n"})
	if _, err := m.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	lis := &controlledNotifyListener{}
	lis.fail.Store(true)
	nrt := &notifyRuntime{lis: &notifyCloseListener{Listener: lis}, done: make(chan struct{})}
	m.mu.Lock()
	proc := m.units[name].proc
	m.units[name].notify = nrt
	m.mu.Unlock()
	if _, err := m.stopUnit(name); err == nil {
		t.Fatal("notification close failure reported success")
	}
	m.mu.Lock()
	rt := m.units[name]
	retained := rt.notify == nrt && rt.proc == nil && rt.cleanupPending()
	m.mu.Unlock()
	if !retained || proc.Alive() {
		t.Fatal("failed notification close lost ownership or prevented process termination")
	}
	st, err := m.Status(name)
	if err != nil || !st.Unit.TerminationUncertain || len(st.Unit.PendingCleanup) != 1 || st.Unit.PendingCleanup[0] != "notification" {
		t.Fatalf("confirmed process cleanup conflated with notification failure: %+v %v", st, err)
	}
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("unresolved notification cleanup admitted replacement")
	}
	lis.fail.Store(false)
	if _, err := m.stopUnit(name); err != nil {
		t.Fatal("notification retry", err)
	}
	m.mu.Lock()
	retained = m.units[name].notify != nil || m.units[name].proc != nil || m.units[name].cleanupPending()
	m.mu.Unlock()
	if retained || lis.calls.Load() != 2 {
		t.Fatal("notification retry did not finish cleanup")
	}
}

func TestNotificationCloseWaitsForEveryCaller(t *testing.T) {
	serveDone := make(chan struct{})
	r := &notifyRuntime{done: make(chan struct{}), serveDone: serveDone}
	finished := make(chan error, 2)
	go func() { finished <- r.Close() }()
	<-r.done
	go func() { finished <- r.Close() }()
	select {
	case err := <-finished:
		close(serveDone)
		t.Fatalf("close returned before server exit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(serveDone)
	for i := 0; i < 2; i++ {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
}

func TestNotificationListenerCloseSerializesCancellation(t *testing.T) {
	lis := &controlledNotifyListener{}
	w := &notifyCloseListener{Listener: lis}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if lis.calls.Load() != 1 {
		t.Fatal("cancellation repeated successful listener close")
	}
}

func TestNotificationFailureDuringExitAndWatchdogRetainsOwnership(t *testing.T) {
	for _, cause := range []string{"exit", "watchdog"} {
		t.Run(cause, func(t *testing.T) {
			const name = "notification-health.service"
			m := managerWith(t, &fakeLauncher{}, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\worker.exe\nRestart=always\n"})
			if _, err := m.Start(context.Background(), name); err != nil {
				t.Fatal(err)
			}
			lis := &controlledNotifyListener{}
			lis.fail.Store(true)
			t.Cleanup(func() { lis.fail.Store(false) })
			nrt := &notifyRuntime{lis: &notifyCloseListener{Listener: lis}, done: make(chan struct{})}
			m.mu.Lock()
			proc := m.units[name].proc.(*fakeProc)
			gen := m.units[name].gen
			owner := runtimeIdentity{name: name, record: m.units[name], gen: gen}
			m.units[name].notify = nrt
			m.mu.Unlock()
			if cause == "exit" {
				proc.die(1)
			} else {
				m.onWatchdogTimeout(owner)
			}
			waitUntil(t, time.Second, func() bool {
				m.mu.Lock()
				defer m.mu.Unlock()
				rt := m.units[name]
				return rt.proc == nil && rt.cleanup == cleanupNotify && strings.HasPrefix(rt.err, "notification cleanup:")
			})
			m.mu.Lock()
			rt := m.units[name]
			retained := rt.notify == nrt && rt.proc == nil && rt.cleanup == cleanupNotify && rt.state == core.Failed
			m.mu.Unlock()
			if !retained || proc.Alive() {
				t.Fatal("health cleanup lost ownership or left process alive")
			}
			if _, err := m.Start(context.Background(), name); err == nil {
				t.Fatal("failed cleanup allowed restart")
			}
			lis.fail.Store(false)
			if _, err := m.stopUnit(name); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNotificationReadinessFailureRetainsListener(t *testing.T) {
	const name = "notification-ready.service"
	m := managerWith(t, &fakeLauncher{}, map[string]string{name: "[Service]\nType=notify\nExecStart=C:\\Tools\\worker.exe\nTimeoutStartSec=50ms\n"})
	lis := &controlledNotifyListener{}
	lis.fail.Store(true)
	t.Cleanup(func() { lis.fail.Store(false) })
	m.cfg.NotifyListen = func(string) (notify.Listener, error) { return lis, nil }
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("missing readiness reported success")
	}
	m.mu.Lock()
	rt := m.units[name]
	retained := rt.notify != nil && rt.proc == nil && rt.cleanup == cleanupNotify
	m.mu.Unlock()
	if !retained {
		t.Fatal("readiness cleanup discarded failed listener")
	}
	lis.fail.Store(false)
	if _, err := m.stopUnit(name); err != nil {
		t.Fatal(err)
	}
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
	retained := rt != nil && rt.hub != nil && rt.cleanupPending() && rt.unavailable
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
	retained = m.units[name].hub != nil || m.units[name].cleanupPending()
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
	retained := m.units[name].hub != nil && m.units[name].cleanupPending()
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
		pending := m.stops.pending[stopKey{notify: nrt}]
		m.stops.mu.Unlock()
		if pending == nil {
			t.Fatal("deadline discarded pending notification close")
		}
		if first == nil {
			first = pending
		} else if pending != first {
			t.Fatal("retry duplicated blocked notification close")
		}
	}
	m.mu.Lock()
	retained := m.units[name].proc == nil && m.units[name].cleanup&cleanupNotify != 0 && m.units[name].cleanup&cleanupWorkload == 0
	m.mu.Unlock()
	if !retained {
		t.Fatal("pending control close lost notification ownership or retained the terminated workload")
	}
	if proc.Alive() {
		t.Fatal("blocked notification close delayed workload termination")
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
