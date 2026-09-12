package manager

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

const maxStopHelpers = 32

// A pending launch and any returned job remain owned independently of the main
// workload and caller deadline. Retries join this attempt, never rerun ExecStop.
type stopHelperWork struct {
	owner      runtimeIdentity
	invocation string
	unit       *unit.Unit
	main       runtime.Process
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	err        error            // published by closing done
	process    runtime.Process  // manager mutex
	capture    *journal.Capture // published by closing done; retained until joined
	reserve    time.Duration
}

func (m *Manager) acceptStopHelper(ctx context.Context, owner runtimeIdentity, u *unit.Unit, main runtime.Process, eligible bool, reserve time.Duration) (*stopHelperWork, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !owner.currentLocked(m) {
		return nil, false, fmt.Errorf("stop helper owner superseded")
	}
	rt := owner.record
	if rt.stopHelper != nil {
		return rt.stopHelper, false, nil
	}
	if !eligible || u == nil || u.Service == nil || len(u.Service.ExecStop) == 0 || rt.invocation == "" || rt.stopHelperAttempt == rt.invocation {
		return nil, false, nil
	}
	if m.closed {
		return nil, false, fmt.Errorf("manager is closing")
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if len(m.stopHelpers) >= maxStopHelpers {
		return nil, false, fmt.Errorf("stop helper capacity %d exhausted", maxStopHelpers)
	}
	workCtx, cancel := context.WithCancel(ctx)
	h := &stopHelperWork{owner: owner, invocation: rt.invocation, unit: u, main: main, ctx: workCtx, cancel: cancel, done: make(chan struct{}), reserve: reserve}
	if m.stopHelpers == nil {
		m.stopHelpers = make(map[*stopHelperWork]struct{})
	}
	m.stopHelpers[h] = struct{}{}
	rt.stopHelper = h
	rt.stopHelperAttempt = rt.invocation
	rt.operations++
	rt.setCleanup(cleanupHelper, true)
	go m.executeStopHelper(h)
	return h, true, nil
}

func (m *Manager) publishStopHelperProcess(h *stopHelperWork, p runtime.Process) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h.process = p
}

func (m *Manager) releaseStopHelper(h *stopHelperWork, p runtime.Process, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, owned := m.stopHelpers[h]; !owned || h.process != p {
		return
	}
	if err != nil {
		h.owner.record.err = fmt.Sprintf("stop helper cleanup: %v", err)
		return
	}
	h.process = nil
	delete(m.stopHelpers, h)
	rt := h.owner.record
	if rt.stopHelper == h {
		rt.stopHelper = nil
		rt.setCleanup(cleanupHelper, false)
	}
	rt.operations--
}

func (m *Manager) executeStopHelper(h *stopHelperWork) {
	defer close(h.done)
	defer h.cancel()
	svc := h.unit.Service
	inv := h.invocation + "-stop"
	env := journal.InjectEnv(mergeEnv(svc.Environment), inv)
	// Never inherit an unrelated parent's MAINPID. No argv expansion or shell
	// is implied; cooperative scripts can read this environment variable.
	filtered := env[:0]
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(name, "MAINPID") {
			filtered = append(filtered, entry)
		}
	}
	env = filtered
	if h.main != nil && h.main.Alive() {
		env = append(env, "MAINPID="+strconv.Itoa(h.main.PID()))
	}
	p, launchErr := m.launch.Start(h.ctx, runtime.StartSpec{Unit: h.owner.name, Type: unit.TypeOneshot, Argv: append([]string(nil), svc.ExecStop...), Dir: svc.WorkingDirectory, Env: env, Limits: runtime.JobLimitsFromSpec(svc)})
	m.publishStopHelperProcess(h, p)
	var capture *journal.Capture
	if p != nil {
		capture = m.journal.AttachConcurrent(h.owner.name, p.PID(), inv, p.Stdout(), p.Stderr())
	}
	h.capture = capture
	var waitErr error
	if launchErr == nil && p != nil {
		waitErr = p.Wait(h.ctx)
	}
	if launchErr == nil && p == nil {
		launchErr = fmt.Errorf("launcher returned no stop helper")
	}
	cleanupCtx, cancel := m.clockTimeout(context.Background(), h.reserve)
	defer cancel()
	cleanupErr := m.stopProcessContext(cleanupCtx, p, h.reserve)
	if cleanupErr == nil && p != nil && p.Alive() {
		cleanupErr = fmt.Errorf("stop helper remains alive")
	}
	var captureErr error
	if cleanupErr == nil && !capture.WaitContext(cleanupCtx) {
		captureErr = fmt.Errorf("stop helper output finalization did not complete")
	}
	m.releaseStopHelper(h, p, errors.Join(cleanupErr, captureErr))
	h.err = errors.Join(launchErr, waitErr, cleanupErr, captureErr)
}

func (m *Manager) retryStopHelperCleanup(ctx context.Context, h *stopHelperWork, timeout time.Duration) error {
	m.mu.Lock()
	p := h.process
	m.mu.Unlock()
	err := m.stopProcessContext(ctx, p, timeout)
	if err == nil && p != nil && p.Alive() {
		err = fmt.Errorf("stop helper remains alive")
	}
	if err == nil && !h.capture.WaitContext(ctx) {
		err = fmt.Errorf("stop helper output finalization did not complete")
	}
	m.releaseStopHelper(h, p, err)
	return err
}

// A helper launch may ignore cancellation. Only this response wait is bounded;
// the worker retains its slot and late process until cleanup is confirmed.
func (m *Manager) cooperativeStop(ctx context.Context, owner runtimeIdentity, u *unit.Unit, main runtime.Process, eligible bool) error {
	total := m.remainingStopBudget(ctx, stopTimeout(u))
	reserve := stopForceReserve(total)
	if reserve <= 0 {
		return ctx.Err()
	}
	grace, cancel := m.clockTimeout(ctx, total-reserve)
	defer cancel()
	h, fresh, err := m.acceptStopHelper(grace, owner, u, main, eligible, reserve)
	if err != nil || h == nil {
		return err
	}
	select {
	case <-h.done:
		if !fresh {
			return m.retryStopHelperCleanup(ctx, h, m.remainingStopBudget(ctx, total))
		}
		if h.err != nil {
			return fmt.Errorf("ExecStop: %w", h.err)
		}
	case <-grace.Done():
		return fmt.Errorf("ExecStop cooperative deadline exceeded: %w", grace.Err())
	}
	if main != nil {
		_ = main.Wait(grace)
		if grace.Err() != nil {
			return fmt.Errorf("workload did not exit within cooperative deadline: %w", grace.Err())
		}
	}
	return nil
}
