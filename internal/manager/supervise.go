package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

const (
	defaultStopTimeout  = 5 * time.Second
	defaultRestartDelay = 100 * time.Millisecond
)

func (m *Manager) startOne(ctx context.Context, name string) error {
	return m.launchUnit(ctx, name, false)
}

func (m *Manager) launchUnit(ctx context.Context, name string, autoRestart bool) error {
	m.mu.Lock()
	if autoRestart && m.stopping[name] {
		m.mu.Unlock()
		return nil
	}
	if !autoRestart {
		m.stopping[name] = false
		m.gens[name]++
		m.cancelRestartLocked(name)
	}
	ld := m.units[name]
	if live := m.procs[name]; live != nil && live.Alive() {
		m.mu.Unlock()
		return nil
	}
	delete(m.procs, name)
	m.mu.Unlock()

	if ld == nil || ld.unit == nil {
		return fmt.Errorf("unit %q is not loaded", name)
	}
	u := ld.unit
	if u.Kind == unit.KindTimer {
		m.armTimer(u)
		return nil
	}
	if u.Kind != unit.KindService || u.Service == nil {
		return nil
	}
	svc := u.Service
	spec := runtime.StartSpec{
		Unit:         name,
		Type:         svc.Type,
		Argv:         append([]string(nil), svc.ExecStart...),
		Dir:          svc.WorkingDirectory,
		Env:          mergeEnv(svc.Environment),
		TimeoutStart: svc.TimeoutStartSec,
	}

	proc, err := m.launch.Start(ctx, spec)
	if err != nil {
		var st *runtime.ExitStatus
		if errors.As(err, &st) {
			m.maybeRestart(name, classifyWait(err), svc)
		}
		return err
	}
	m.journal.Attach(name, proc.PID(), proc.Stdout(), proc.Stderr())

	m.mu.Lock()
	if m.stopping[name] {
		m.mu.Unlock()
		_ = proc.Stop(0)
		return nil
	}
	if existing := m.procs[name]; existing != nil && existing.Alive() {
		m.mu.Unlock()
		_ = proc.Stop(0)
		return nil
	}
	m.procs[name] = proc
	if autoRestart {
		st, sub := core.Step(m.stateOfLocked(name), m.subOfLocked(name), core.EventStartSucceeded)
		if proc.Alive() {
			m.states[name] = st
			m.subs[name] = sub
			delete(m.errors, name)
		}
	}
	m.mu.Unlock()

	if m.engine != nil {
		m.engine.UnitActive(name, time.Now())
	}

	if svc.Type == unit.TypeOneshot && proc.Alive() {
		// Stub/fake oneshot that has not exited: stay Active like M5.
		return nil
	}
	go m.watch(name, proc)
	return nil
}

func (m *Manager) watch(name string, proc runtime.Process) {
	// Wait for the main process; then tear down the unit job so leftover
	// children die. Restart CreateProcess into a new per-unit job.
	err := proc.Wait(context.Background())
	m.mu.Lock()
	if m.procs[name] != proc {
		m.mu.Unlock()
		return
	}
	delete(m.procs, name)
	stopping := m.stopping[name]
	ld := m.units[name]
	gen := m.gens[name]
	m.mu.Unlock()

	if job := proc.Job(); job != nil {
		_ = job.Kill()
	}
	_ = proc.Close()

	if stopping {
		return
	}

	var svc *unit.ServiceSpec
	if ld != nil && ld.unit != nil {
		svc = ld.unit.Service
	}
	kind := classifyWait(err)
	if svc != nil && core.ShouldRestart(svc.Restart, kind) {
		m.beginRestart(name, gen, restartDelay(svc))
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping[name] || m.gens[name] != gen {
		return
	}
	oneshot := svc != nil && svc.Type == unit.TypeOneshot
	if oneshot && kind == core.ExitSuccess {
		return
	}
	st, sub := core.Step(m.stateOfLocked(name), m.subOfLocked(name), core.EventMainExited)
	m.states[name] = st
	m.subs[name] = sub
	m.errors[name] = "main process exited"
}

func (m *Manager) maybeRestart(name string, kind core.ExitKind, svc *unit.ServiceSpec) {
	if svc == nil || !core.ShouldRestart(svc.Restart, kind) {
		return
	}
	m.mu.Lock()
	if m.stopping[name] {
		m.mu.Unlock()
		return
	}
	gen := m.gens[name]
	m.mu.Unlock()
	go m.beginRestart(name, gen, restartDelay(svc))
}

func (m *Manager) beginRestart(name string, gen uint64, delay time.Duration) {
	m.mu.Lock()
	if m.stopping[name] || m.gens[name] != gen {
		m.mu.Unlock()
		return
	}
	st, sub := core.Step(m.stateOfLocked(name), m.subOfLocked(name), core.EventAutoRestart)
	m.states[name] = st
	m.subs[name] = sub
	delete(m.errors, name)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelRestartLocked(name)
	m.cancels[name] = cancel
	m.mu.Unlock()

	if delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	} else if ctx.Err() != nil {
		return
	}

	m.mu.Lock()
	if m.stopping[name] || m.gens[name] != gen {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	_ = m.launchUnit(context.Background(), name, true)
}

func (m *Manager) cancelRestartLocked(name string) {
	if c := m.cancels[name]; c != nil {
		c()
		delete(m.cancels, name)
	}
}

func (m *Manager) subOfLocked(name string) core.Substate {
	if s, ok := m.subs[name]; ok {
		return s
	}
	return core.SubNone
}

func (m *Manager) reapFailedLocked() {
	for name, st := range m.states {
		if st != core.Failed {
			continue
		}
		proc := m.procs[name]
		if proc == nil {
			continue
		}
		delete(m.procs, name)
		go func(p runtime.Process) {
			_ = p.Stop(defaultStopTimeout)
		}(proc)
	}
}

func classifyWait(err error) core.ExitKind {
	if err == nil {
		return core.ExitSuccess
	}
	var st *runtime.ExitStatus
	if errors.As(err, &st) {
		if st.Failed() {
			return core.ExitFailure
		}
		return core.ExitSuccess
	}
	return core.ExitAbnormal
}

func restartDelay(svc *unit.ServiceSpec) time.Duration {
	if svc != nil && svc.RestartSecSet {
		return svc.RestartSec
	}
	return defaultRestartDelay
}

func stopTimeout(u *unit.Unit) time.Duration {
	if u != nil && u.Service != nil && u.Service.TimeoutStopSecSet {
		return u.Service.TimeoutStopSec
	}
	return defaultStopTimeout
}

func mergeEnv(extra []unit.EnvVar) []string {
	base := os.Environ()
	if len(extra) == 0 {
		return base
	}
	index := make(map[string]int, len(base))
	for i, e := range base {
		name, _, _ := strings.Cut(e, "=")
		index[envKey(name)] = i
	}
	for _, v := range extra {
		kv := v.Name + "=" + v.Value
		if i, ok := index[envKey(v.Name)]; ok {
			base[i] = kv
			continue
		}
		index[envKey(v.Name)] = len(base)
		base = append(base, kv)
	}
	return base
}

func envKey(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return unicode.ToUpper(r)
		}
		return r
	}, name)
}
