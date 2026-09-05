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
	name      string
	lis       notify.Listener
	cancel    context.CancelFunc
	ready     chan struct{}
	pulse     chan struct{}
	done      chan struct{}
	serveDone chan struct{}
	mu        sync.Mutex
	status    string
	mainPID   int
	job       runtime.Job
	readySet  bool
	closed    bool
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
		name:      name,
		lis:       lis,
		cancel:    cancel,
		ready:     make(chan struct{}),
		pulse:     make(chan struct{}, 8),
		done:      make(chan struct{}),
		serveDone: make(chan struct{}),
	}
	go func() {
		defer close(rt.serveDone)
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
	// Wait for ServeAccept to leave Accept so Windows can re-Listen
	// the same \\.\pipe\winunitd\notify\<unit> name on restart.
	if r.serveDone != nil {
		<-r.serveDone
	}
}

func (m *Manager) closeNotify(name string) {
	m.mu.Lock()
	var nrt *notifyRuntime
	if rt := m.units[name]; rt != nil {
		nrt = rt.notify
		rt.notify = nil
	}
	m.mu.Unlock()
	if nrt != nil {
		nrt.Close()
	}
}

func (m *Manager) waitReady(ctx context.Context, name string, proc runtime.Process, timeout time.Duration) error {
	m.mu.Lock()
	var nrt *notifyRuntime
	if rt := m.units[name]; rt != nil {
		nrt = rt.notify
	}
	m.mu.Unlock()
	if nrt == nil {
		return fmt.Errorf("notify listener missing for %s", name)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := m.clockTimeout(ctx, timeout)
	defer cancel()

	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-nrt.ready:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("TimeoutStartSec exceeded waiting for READY=1: %w", ctx.Err())
		case <-nrt.done:
			return fmt.Errorf("notify pipe closed before READY=1")
		case <-tick.C:
			m.mu.Lock()
			stopping := false
			if rt := m.units[name]; rt != nil {
				stopping = rt.stopping
			}
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
	if rt := m.units[name]; rt != nil && !m.closed {
		rt.watchdog = cancel
		m.mu.Unlock()
	} else {
		m.mu.Unlock()
		cancel()
		return
	}
	switch svc.WatchdogMode {
	case unit.WatchdogModeTCP, unit.WatchdogModeHTTP:
		go m.probeWatchdogLoop(ctx, name, svc, gen)
	default:
		go m.watchdogLoop(ctx, name, svc.WatchdogSec, gen)
	}
}

func (m *Manager) stopWatchdog(name string) {
	m.mu.Lock()
	var cancel context.CancelFunc
	if rt := m.units[name]; rt != nil {
		cancel = rt.watchdog
		rt.watchdog = nil
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) watchdogLoop(ctx context.Context, name string, interval time.Duration, gen uint64) {
	m.mu.Lock()
	var nrt *notifyRuntime
	if rt := m.units[name]; rt != nil {
		nrt = rt.notify
	}
	m.mu.Unlock()
	if nrt == nil {
		return
	}
	timer := m.clock().Timer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-nrt.done:
			return
		case <-nrt.pulse:
			if !timer.Stop() {
				select {
				case <-timer.C():
				default:
				}
			}
			timer.Reset(interval)
		case <-timer.C():
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
	timer := m.clock().Timer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C():
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
	unlock := m.ops.lock(name)
	released := false
	release := func() {
		if !released {
			released = true
			unlock()
		}
	}
	defer release()
	m.mu.Lock()
	rt := m.units[name]
	if rt == nil || rt.stopping || rt.gen != gen || m.closed || rt.stopUncertain {
		m.mu.Unlock()
		return
	}
	if !rt.step(core.EventWatchdogFailed) {
		m.mu.Unlock()
		return
	}
	rt.err = "watchdog timed out"
	rt.terminated = true
	u := rt.unit
	proc := rt.proc
	rt.stopUncertain = proc != nil
	nrt := rt.notify
	rt.notify = nil
	wdCancel := rt.watchdog
	rt.watchdog = nil
	m.mu.Unlock()
	if wdCancel != nil {
		wdCancel()
	}
	if nrt != nil {
		nrt.Close()
	}

	stopErr := m.stopProcess(proc, stopTimeout(u))
	if stopErr == nil && proc != nil && proc.Alive() {
		stopErr = fmt.Errorf("process remains alive after watchdog cleanup")
	}
	m.mu.Lock()
	rt = m.units[name]
	if rt == nil || !rt.sameOp(gen, proc) {
		m.mu.Unlock()
		return
	}
	if stopErr != nil {
		rt.err = fmt.Sprintf("watchdog cleanup: %v", stopErr)
		m.mu.Unlock()
		return
	}
	rt.proc = nil
	rt.stopUncertain = false
	rt.terminated = false
	m.mu.Unlock()
	release()

	var svc *unit.ServiceSpec
	if u != nil {
		svc = u.Service
	}
	if svc != nil && core.ShouldRestart(svc.Restart, core.ExitWatchdog) {
		m.beginRestart(name, gen, restartDelay(svc))
	}
}
