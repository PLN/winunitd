package manager

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

const defaultStopTimeout = 5 * time.Second

func (m *Manager) startOne(ctx context.Context, name string) error {
	m.mu.Lock()
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
		return err
	}
	journal.Attach(name, proc.Stdout(), proc.Stderr())

	m.mu.Lock()
	if existing := m.procs[name]; existing != nil && existing.Alive() {
		m.mu.Unlock()
		_ = proc.Stop(0)
		return nil
	}
	m.procs[name] = proc
	m.mu.Unlock()

	if svc.Type != unit.TypeOneshot {
		go m.watch(name, proc)
	}
	return nil
}

func (m *Manager) watch(name string, proc runtime.Process) {
	// Wait for the main process; then tear down the unit job so leftover
	// children die. No restart (M6). Stop may already own the process.
	_ = proc.Wait(context.Background())
	m.mu.Lock()
	if m.procs[name] != proc {
		m.mu.Unlock()
		return
	}
	delete(m.procs, name)
	switch m.stateOfLocked(name) {
	case core.Active, core.Activating:
		m.states[name] = core.Failed
		m.errors[name] = "main process exited"
	}
	m.mu.Unlock()
	if job := proc.Job(); job != nil {
		_ = job.Kill()
	}
	_ = proc.Close()
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
