package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

type loaded struct {
	unit    *unit.Unit
	enabled bool
	targets []string
}

// Manager holds loaded units and serves the control protocol.
type Manager struct {
	cfg    Config
	launch runtime.Launcher
	mu     sync.Mutex
	units  map[string]*loaded
	graph  *core.Graph
	states map[string]core.State
	errors map[string]string
	procs  map[string]runtime.Process
}

// New creates a manager. Reload must be called to load units.
func New(cfg Config) (*Manager, error) {
	if cfg.BaseDir == "" {
		return nil, fmt.Errorf("base directory required")
	}
	launch := cfg.Launch
	if launch == nil {
		launch = runtime.NewLauncher(cfg.Daemon)
	}
	return &Manager{
		cfg:    cfg,
		launch: launch,
		units:  make(map[string]*loaded),
		states: make(map[string]core.State),
		errors: make(map[string]string),
		procs:  make(map[string]runtime.Process),
	}, nil
}

// Handle implements protocol.Handler.
func (m *Manager) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case protocol.MethodListUnits:
		var p protocol.ListUnitsParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return m.ListUnits()
	case protocol.MethodListTimers:
		var p protocol.ListTimersParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return m.ListTimers()
	case protocol.MethodStatus:
		var p protocol.StatusParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return m.Status(p.Unit)
	case protocol.MethodStart:
		var p protocol.UnitParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return m.Start(ctx, p.Unit)
	case protocol.MethodStop:
		var p protocol.UnitParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return m.Stop(p.Unit)
	case protocol.MethodRestart:
		var p protocol.UnitParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return m.Restart(ctx, p.Unit)
	case protocol.MethodEnable:
		var p protocol.UnitParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return m.Enable(p.Unit)
	case protocol.MethodDisable:
		var p protocol.UnitParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return m.Disable(p.Unit)
	case protocol.MethodLogs:
		var p protocol.LogsParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return m.Logs(p)
	case protocol.MethodDaemonReload:
		var p protocol.DaemonReloadParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return m.Reload()
	case protocol.MethodVerify:
		var p protocol.VerifyParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return m.Verify(p.Unit)
	default:
		return nil, protocol.ErrMethodNotFound(method)
	}
}

func requireUnit(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", protocol.ErrInvalidParams("unit name required")
	}
	return core.NormalizeName(name), nil
}

func (m *Manager) lookup(name string) (*loaded, error) {
	name, err := requireUnit(name)
	if err != nil {
		return nil, err
	}
	ld, ok := m.units[name]
	if !ok {
		return nil, protocol.ErrNotFound(name)
	}
	return ld, nil
}

// ListUnits returns loaded units in name order.
func (m *Manager) ListUnits() (*protocol.ListUnitsResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := m.names()
	out := make([]protocol.UnitStatus, 0, len(names))
	for _, name := range names {
		out = append(out, m.unitStatusLocked(name))
	}
	return &protocol.ListUnitsResult{Units: out}, nil
}

// ListTimers returns loaded timer units.
func (m *Manager) ListTimers() (*protocol.ListTimersResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []protocol.TimerStatus
	for _, name := range m.names() {
		ld := m.units[name]
		if ld.unit.Kind != unit.KindTimer {
			continue
		}
		st := m.unitStatusLocked(name)
		activated := ""
		if ld.unit.Timer != nil {
			activated = ld.unit.Timer.Unit
		}
		out = append(out, protocol.TimerStatus{
			Name:        st.Name,
			Description: st.Description,
			Path:        st.Path,
			LoadState:   st.LoadState,
			ActiveState: st.ActiveState,
			Enabled:     st.Enabled,
			Unit:        activated,
		})
	}
	return &protocol.ListTimersResult{Timers: out}, nil
}

// Status returns machine status or a single unit.
func (m *Manager) Status(name string) (*protocol.StatusResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(name) == "" {
		return &protocol.StatusResult{Machine: m.machineLocked()}, nil
	}
	ld, err := m.lookup(name)
	if err != nil {
		return nil, err
	}
	st := m.unitStatusLocked(ld.unit.Name)
	return &protocol.StatusResult{Unit: &st}, nil
}

func (m *Manager) machineLocked() *protocol.MachineStatus {
	ms := &protocol.MachineStatus{State: "running"}
	for name, ld := range m.units {
		ms.UnitsLoaded++
		if ld.unit.Kind == unit.KindTimer {
			ms.TimersLoaded++
		}
		switch m.stateOfLocked(name) {
		case core.Active, core.Activating:
			ms.UnitsActive++
		case core.Failed:
			ms.UnitsFailed++
		}
	}
	return ms
}

func (m *Manager) unitStatusLocked(name string) protocol.UnitStatus {
	ld := m.units[name]
	st := protocol.UnitStatus{
		Name:        name,
		LoadState:   "loaded",
		ActiveState: m.stateOfLocked(name).String(),
		Enabled:     ld.enabled,
	}
	if ld.unit != nil {
		st.Description = ld.unit.Description
		st.Kind = string(ld.unit.Kind)
		st.Path = ld.unit.Path
	}
	if err := m.errors[name]; err != "" {
		st.Error = err
	}
	return st
}

func (m *Manager) stateOfLocked(name string) core.State {
	if s, ok := m.states[name]; ok {
		return s
	}
	return core.Inactive
}

func (m *Manager) names() []string {
	names := make([]string, 0, len(m.units))
	for name := range m.units {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Start runs a start transaction, then CreateProcess into a per-unit job.
func (m *Manager) Start(ctx context.Context, name string) (*protocol.UnitResult, error) {
	name, err := requireUnit(name)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	g := m.graph
	if _, ok := m.units[name]; !ok {
		m.mu.Unlock()
		return nil, protocol.ErrNotFound(name)
	}
	m.mu.Unlock()
	if g == nil {
		return nil, protocol.ErrFailed("no units loaded")
	}

	run, err := g.Start(ctx, core.StartFunc(m.startOne), name)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyRunLocked(run)
	m.reapFailedLocked()
	if err != nil {
		m.errors[name] = err.Error()
		return &protocol.UnitResult{
			Unit:        name,
			ActiveState: m.stateOfLocked(name).String(),
			Error:       err.Error(),
		}, protocol.ErrFailed(err.Error())
	}
	delete(m.errors, name)
	return &protocol.UnitResult{Unit: name, ActiveState: m.stateOfLocked(name).String()}, nil
}

func (m *Manager) applyRunLocked(run *core.Run) {
	if run == nil {
		return
	}
	for name, st := range run.States {
		m.states[name] = st
	}
	for name, err := range run.Errors {
		if err != nil {
			m.errors[name] = err.Error()
		}
	}
}

// Stop kills the unit Job Object so the whole process tree dies.
func (m *Manager) Stop(name string) (*protocol.UnitResult, error) {
	m.mu.Lock()
	ld, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	name = ld.unit.Name
	proc := m.procs[name]
	delete(m.procs, name)
	timeout := stopTimeout(ld.unit)
	m.states[name] = core.Deactivating
	delete(m.errors, name)
	m.mu.Unlock()

	if proc != nil {
		_ = proc.Stop(timeout)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[name] = core.Inactive
	return &protocol.UnitResult{Unit: name, ActiveState: core.Inactive.String()}, nil
}

// Restart is stop then start. Restart= policy is M6.
func (m *Manager) Restart(ctx context.Context, name string) (*protocol.UnitResult, error) {
	if _, err := m.Stop(name); err != nil {
		return nil, err
	}
	return m.Start(ctx, name)
}

// Enable writes tiny link files under enabled/<target>/ (DESIGN.md §12).
func (m *Manager) Enable(name string) (*protocol.EnableResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ld, err := m.lookup(name)
	if err != nil {
		return nil, err
	}
	name = ld.unit.Name
	targets := ld.unit.WantedBy
	if len(targets) == 0 {
		targets = []string{defaultTarget}
	}
	normalized := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		t = core.NormalizeName(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		normalized = append(normalized, t)
	}
	sort.Strings(normalized)
	for _, t := range normalized {
		path := m.cfg.EnabledPath(t, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, protocol.ErrFailed(err.Error())
		}
		if err := os.WriteFile(path, []byte(name+"\n"), 0o644); err != nil {
			return nil, protocol.ErrFailed(err.Error())
		}
	}
	ld.enabled = true
	ld.targets = normalized
	return &protocol.EnableResult{Unit: name, Enabled: true, Targets: normalized}, nil
}

// Disable removes enable files for the unit.
func (m *Manager) Disable(name string) (*protocol.EnableResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ld, err := m.lookup(name)
	if err != nil {
		return nil, err
	}
	name = ld.unit.Name
	_ = filepath.WalkDir(m.cfg.EnabledDir(), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == name {
			_ = os.Remove(path)
		}
		return nil
	})
	ld.enabled = false
	ld.targets = nil
	return &protocol.EnableResult{Unit: name, Enabled: false}, nil
}

// Logs returns an empty journal snapshot. Follow is ignored (no journal yet).
func (m *Manager) Logs(p protocol.LogsParams) (*protocol.LogsResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ld, err := m.lookup(p.Unit)
	if err != nil {
		return nil, err
	}
	return &protocol.LogsResult{Unit: ld.unit.Name, Entries: []protocol.LogEntry{}}, nil
}

// Verify re-reads a loaded unit file (daemon-side; path verify stays in winctl).
func (m *Manager) Verify(name string) (*protocol.VerifyResult, error) {
	m.mu.Lock()
	ld, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	path := ld.unit.Path
	unitName := ld.unit.Name
	m.mu.Unlock()

	rep := unit.VerifyPath(path)
	out := &protocol.VerifyResult{Name: unitName, OK: !rep.HasError()}
	for _, iss := range rep.Issues {
		out.Issues = append(out.Issues, protocol.Issue{
			Path:     iss.Path,
			Line:     iss.Line,
			Severity: string(iss.Severity),
			Message:  iss.Message,
		})
	}
	return out, nil
}

func (m *Manager) enabledTargets(name string) []string {
	dir := m.cfg.EnabledDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var targets []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), name)); err == nil {
			targets = append(targets, e.Name())
		}
	}
	sort.Strings(targets)
	return targets
}
