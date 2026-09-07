package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

type notifyRuntime struct {
	closeMu   sync.Mutex
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
		if lis != nil {
			return &notifyRuntime{name: name, lis: &notifyCloseListener{Listener: lis}}, err
		}
		return nil, err
	}
	if lis == nil {
		return nil, fmt.Errorf("notification open returned no listener")
	}
	lis = &notifyCloseListener{Listener: lis}
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

// notifyCloseListener serializes ServeAccept cancellation with manager cleanup.
// A failed close remains retryable; a successful close is never repeated.
type notifyCloseListener struct {
	notify.Listener
	mu      sync.Mutex
	closed  bool
	stateMu sync.Mutex
	closing bool
	accepts sync.WaitGroup
	clients map[*notifyOwnedConn]struct{}
}

func (l *notifyCloseListener) Accept() (notify.Conn, error) {
	l.stateMu.Lock()
	if l.closing {
		l.stateMu.Unlock()
		return nil, net.ErrClosed
	}
	l.accepts.Add(1)
	l.stateMu.Unlock()
	defer l.accepts.Done()
	c, err := l.Listener.Accept()
	if c == nil {
		return nil, err
	}
	owned := &notifyOwnedConn{Conn: c, owner: l}
	l.stateMu.Lock()
	if l.clients == nil {
		l.clients = make(map[*notifyOwnedConn]struct{})
	}
	l.clients[owned] = struct{}{}
	l.stateMu.Unlock()
	if err != nil {
		return nil, errors.Join(err, owned.Close())
	}
	return owned, nil
}

type notifyOwnedConn struct {
	notify.Conn
	owner  *notifyCloseListener
	mu     sync.Mutex
	closed bool
}

func (c *notifyOwnedConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	if err := c.Conn.Close(); err != nil && !onlyNetClosed(err) {
		return err
	}
	c.closed = true
	c.owner.stateMu.Lock()
	delete(c.owner.clients, c)
	c.owner.stateMu.Unlock()
	return nil
}

// A joined cleanup failure must not be hidden by one already-closed leaf.
func onlyNetClosed(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !onlyNetClosed(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyNetClosed(wrapped.Unwrap())
	}
	return errors.Is(err, net.ErrClosed)
}

func (l *notifyCloseListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stateMu.Lock()
	l.closing = true
	l.stateMu.Unlock()
	var result error
	if !l.closed {
		err := l.Listener.Close()
		if err == nil || onlyNetClosed(err) {
			l.closed = true
		} else {
			result = err
		}
	}
	if l.closed {
		// No new Accept can start after closing was set. Include every late
		// accepted client before deciding that cleanup succeeded.
		l.accepts.Wait()
	}
	l.stateMu.Lock()
	clients := make([]*notifyOwnedConn, 0, len(l.clients))
	for client := range l.clients {
		clients = append(clients, client)
	}
	l.stateMu.Unlock()
	for _, client := range clients {
		result = errors.Join(result, client.Close())
	}
	return result
}

func (r *notifyRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.closeMu.Lock()
	defer r.closeMu.Unlock()
	r.mu.Lock()
	first := !r.closed
	r.closed = true
	r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
	if first && r.done != nil {
		close(r.done)
	}
	if r.lis != nil {
		if err := r.lis.Close(); err != nil && !onlyNetClosed(err) {
			return err
		}
	}
	// A successful native close must also release Accept before name reuse.
	if r.serveDone != nil {
		<-r.serveDone
	}
	return nil
}

func (m *Manager) closeNotify(name string) error {
	return m.closeNotifyContext(context.Background(), name, defaultStopTimeout)
}

func (m *Manager) closeNotifyContext(ctx context.Context, name string, timeout time.Duration) error {
	m.mu.Lock()
	var nrt *notifyRuntime
	if rt := m.units[name]; rt != nil {
		nrt = rt.notify
	}
	m.mu.Unlock()
	if nrt == nil {
		return nil
	}
	err := m.stops.wait(ctx, m.clock(), stopKey{notify: nrt}, timeout, nrt.Close)
	m.applyNotifyCleanup(notifyCleanup{name: name, notify: nrt, err: err})
	return err
}

func (m *Manager) disposeNotify(nrt *notifyRuntime) error {
	if nrt == nil {
		return nil
	}
	m.mu.Lock()
	if rt := m.units[nrt.name]; rt != nil && rt.notify == nil && !m.closed {
		rt.notify = nrt
		m.mu.Unlock()
		return m.closeNotify(nrt.name)
	}
	m.closePending = append(m.closePending, unitTeardown{notify: nrt})
	m.mu.Unlock()
	err := m.stops.wait(context.Background(), m.clock(), stopKey{notify: nrt}, defaultStopTimeout, nrt.Close)
	if err == nil {
		m.mu.Lock()
		kept := m.closePending[:0]
		for _, td := range m.closePending {
			if td.notify != nrt {
				kept = append(kept, td)
			}
		}
		m.closePending = kept
		m.mu.Unlock()
	}
	return err
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
	ctx, cancel := context.WithCancel(context.Background())
	owner, previous, accepted := m.acceptWatchdog(name, gen, cancel)
	if !accepted {
		cancel()
		return
	}
	if previous != nil {
		previous()
	}
	switch svc.WatchdogMode {
	case unit.WatchdogModeTCP, unit.WatchdogModeHTTP:
		go m.probeWatchdogLoop(ctx, owner, svc)
	default:
		go m.watchdogLoop(ctx, owner, svc.WatchdogSec)
	}
}

func (m *Manager) stopWatchdog(name string) {
	cancel := m.detachWatchdog(name)
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) watchdogLoop(ctx context.Context, owner runtimeIdentity, interval time.Duration) {
	m.mu.Lock()
	var nrt *notifyRuntime
	if owner.currentLocked(m) {
		nrt = owner.record.notify
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
			m.onWatchdogTimeout(owner)
			return
		}
	}
}

func (m *Manager) probeWatchdogLoop(ctx context.Context, owner runtimeIdentity, svc *unit.ServiceSpec) {
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
				m.onWatchdogTimeout(owner)
				return
			}
			timer.Reset(interval)
		}
	}
}

func (m *Manager) onWatchdogTimeout(owner runtimeIdentity) {
	name := owner.name
	unlock := m.ops.lock(name)
	released := false
	release := func() {
		if !released {
			released = true
			unlock()
		}
	}
	defer release()
	effect := m.acceptWatchdogFailure(owner)
	if effect == nil {
		return
	}
	u, proc, wdCancel := effect.unit, effect.process, effect.cancel
	if wdCancel != nil {
		wdCancel()
	}
	notifyErr := m.closeNotify(name)
	stopErr := m.stopProcess(proc, stopTimeout(u))
	if stopErr == nil && proc != nil && proc.Alive() {
		stopErr = fmt.Errorf("process remains alive after watchdog cleanup")
	}
	stopErr = errors.Join(stopErr, notifyErr)
	if !m.applyWatchdogCleanup(watchdogCleanup{effect: effect, err: stopErr}) {
		return
	}
	release()

	var svc *unit.ServiceSpec
	if u != nil {
		svc = u.Service
	}
	if svc != nil && core.ShouldRestart(svc.Restart, core.ExitWatchdog) {
		m.beginRestart(recoveryRequest{owner: owner, delay: restartDelay(svc)})
	}
}
