package manager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

type notifyRuntime struct {
	name     string
	lis      notify.Listener
	cancel   context.CancelFunc
	ready    chan struct{}
	pulse    chan struct{}
	done     chan struct{}
	mu       sync.Mutex
	status   string
	mainPID  int
	job      runtime.Job
	readySet bool
	closed   bool
}

func (m *Manager) openNotify(name string) (*notifyRuntime, error) {
	listen := m.cfg.NotifyListen
	if listen == nil {
		sid := m.cfg.NotifySID
		listen = func(id string) (notify.Listener, error) {
			return notify.Listen(id, sid)
		}
	}
	lis, err := listen(name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt := &notifyRuntime{
		name:   name,
		lis:    lis,
		cancel: cancel,
		ready:  make(chan struct{}),
		pulse:  make(chan struct{}, 8),
		done:   make(chan struct{}),
	}
	go func() {
		notify.ServeAccept(ctx, lis, rt.allowed, func(msg notify.Message) {
			rt.onMessage(msg)
		})
	}()
	return rt, nil
}

func (r *notifyRuntime) Addr() string {
	if r == nil || r.lis == nil {
		return ""
	}
	return r.lis.Addr()
}

func (r *notifyRuntime) SetMain(pid int, job runtime.Job) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.mainPID = pid
	r.job = job
	r.mu.Unlock()
}

func (r *notifyRuntime) allowed(pid int) bool {
	if r == nil {
		return false
	}
	if pid <= 0 {
		// Fake TCP listener (Linux tests): no client PID.
		return true
	}
	r.mu.Lock()
	main := r.mainPID
	job := r.job
	r.mu.Unlock()
	if main <= 0 {
		return true
	}
	if pid == main {
		return true
	}
	// NotifyAccess=main: the unit owns its Job Object tree, so
	// winunit-notify.exe spawned by a script is accepted.
	if job != nil {
		ok, err := job.Contains(pid)
		if err == nil && ok {
			return true
		}
	}
	return false
}

func (r *notifyRuntime) onMessage(msg notify.Message) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if msg.Status != "" {
		r.status = msg.Status
	}
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return
	}
	if msg.Ready {
		r.mu.Lock()
		if !r.readySet {
			r.readySet = true
			close(r.ready)
		}
		r.mu.Unlock()
	}
	if msg.Watchdog {
		select {
		case r.pulse <- struct{}{}:
		default:
		}
	}
}

func (r *notifyRuntime) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.mu.Unlock()
	r.cancel()
	if r.lis != nil {
		_ = r.lis.Close()
	}
	select {
	case <-r.done:
	default:
		close(r.done)
	}
}

func (m *Manager) closeNotify(name string) {
	m.mu.Lock()
	rt := m.notifies[name]
	delete(m.notifies, name)
	m.mu.Unlock()
	if rt != nil {
		rt.Close()
	}
}

func (m *Manager) waitReady(ctx context.Context, name string, proc runtime.Process, timeout time.Duration) error {
	m.mu.Lock()
	rt := m.notifies[name]
	m.mu.Unlock()
	if rt == nil {
		return fmt.Errorf("notify listener missing for %s", name)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-rt.ready:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("TimeoutStartSec exceeded waiting for READY=1: %w", ctx.Err())
		case <-deadline:
			return fmt.Errorf("TimeoutStartSec exceeded waiting for READY=1")
		case <-rt.done:
			return fmt.Errorf("notify pipe closed before READY=1")
		case <-tick.C:
			m.mu.Lock()
			stopping := m.stopping[name]
			m.mu.Unlock()
			if stopping {
				return fmt.Errorf("unit stopped before READY=1")
			}
			if proc != nil && !proc.Alive() {
				return fmt.Errorf("main process exited before READY=1")
			}
		}
	}
}

func (m *Manager) startWatchdog(name string, svc *unit.ServiceSpec, gen uint64) {
	if svc == nil || !svc.WatchdogEnabled() {
		return
	}
	m.stopWatchdog(name)
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.watchdogs[name] = cancel
	m.mu.Unlock()
	switch svc.WatchdogMode {
	case unit.WatchdogModeTCP, unit.WatchdogModeHTTP:
		go m.probeWatchdogLoop(ctx, name, svc, gen)
	default:
		go m.watchdogLoop(ctx, name, svc.WatchdogSec, gen)
	}
}

func (m *Manager) stopWatchdog(name string) {
	m.mu.Lock()
	cancel := m.watchdogs[name]
	delete(m.watchdogs, name)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) watchdogLoop(ctx context.Context, name string, interval time.Duration, gen uint64) {
	m.mu.Lock()
	rt := m.notifies[name]
	m.mu.Unlock()
	if rt == nil {
		return
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rt.done:
			return
		case <-rt.pulse:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(interval)
		case <-timer.C:
			m.onWatchdogTimeout(name, gen)
			return
		}
	}
}

func (m *Manager) probeWatchdogLoop(ctx context.Context, name string, svc *unit.ServiceSpec, gen uint64) {
	interval := svc.WatchdogSec
	if interval <= 0 {
		return
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			pctx, cancel := context.WithTimeout(ctx, unit.WatchdogProbeTimeout(interval))
			err := svc.ProbeWatchdog(pctx)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				m.onWatchdogTimeout(name, gen)
				return
			}
			timer.Reset(interval)
		}
	}
}

func (m *Manager) onWatchdogTimeout(name string, gen uint64) {
	m.mu.Lock()
	if m.stopping[name] || m.gens[name] != gen {
		m.mu.Unlock()
		return
	}
	st, sub := core.Step(m.stateOfLocked(name), m.subOfLocked(name), core.EventWatchdogFailed)
	m.states[name] = st
	m.subs[name] = sub
	m.errors[name] = "watchdog timed out"
	m.terminated[name] = true
	ld := m.units[name]
	proc := m.procs[name]
	m.mu.Unlock()

	if proc != nil {
		_ = proc.Stop(0)
	}

	var svc *unit.ServiceSpec
	if ld != nil && ld.unit != nil {
		svc = ld.unit.Service
	}
	if svc != nil && core.ShouldRestart(svc.Restart, core.ExitWatchdog) {
		m.beginRestart(name, gen, restartDelay(svc))
	}
}
