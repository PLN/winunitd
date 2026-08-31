package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
	"github.com/PLN/winunitd/internal/unit"
)

type loaded struct {
	unit    *unit.Unit
	enabled bool
	targets []string
}

// Manager holds loaded units and serves the control protocol.
type Manager struct {
	cfg      Config
	launch   runtime.Launcher
	journal  *journal.Store
	engine   *timers.Engine
	mu       sync.Mutex
	units    map[string]*loaded
	graph    *core.Graph
	states   map[string]core.State
	errors   map[string]string
	procs    map[string]runtime.Process
	subs     map[string]core.Substate
	gens     map[string]uint64
	cancels  map[string]context.CancelFunc
	stopping map[string]bool
	session  sync.Mutex // serializes graphical-session.target start/stop
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
	js, err := journal.Open(cfg.JournalDir())
	if err != nil {
		return nil, err
	}
	store, err := timers.OpenStore(cfg.TimerStateDir())
	if err != nil {
		return nil, err
	}
	clk := cfg.Clock
	if clk.Now == nil || clk.SinceBoot == nil || clk.Startup.IsZero() {
		def := timers.DefaultClock()
		if clk.Now == nil {
			clk.Now = def.Now
		}
		if clk.SinceBoot == nil {
			clk.SinceBoot = def.SinceBoot
		}
		if clk.Startup.IsZero() {
			clk.Startup = def.Startup
		}
	}
	m := &Manager{
		cfg:      cfg,
		launch:   launch,
		journal:  js,
		units:    make(map[string]*loaded),
		states:   make(map[string]core.State),
		errors:   make(map[string]string),
		procs:    make(map[string]runtime.Process),
		subs:     make(map[string]core.Substate),
		gens:     make(map[string]uint64),
		cancels:  make(map[string]context.CancelFunc),
		stopping: make(map[string]bool),
	}
	m.engine = timers.NewEngine(clk, store, m.onTimerElapsed)
	return m, nil
}

// Close stops the timer scheduler.
func (m *Manager) Close() {
	if m == nil || m.engine == nil {
		return
	}
	m.engine.Stop()
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
			Next:        st.Next,
			Last:        st.Last,
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
	if proc := m.procs[name]; proc != nil && proc.Alive() {
		st.MainPID = proc.PID()
	}
	if err := m.errors[name]; err != "" {
		st.Error = err
	}
	if ld.unit != nil && ld.unit.Kind == unit.KindTimer && m.engine != nil {
		snap := m.engine.Status(name)
		st.Next = formatTimerStamp(snap.Next)
		st.Last = formatTimerStamp(snap.Last)
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
		if m.subOfLocked(name) == core.SubAutoRestart {
			continue
		}
		m.states[name] = st
	}
	for name, err := range run.Errors {
		if err != nil {
			m.errors[name] = err.Error()
		}
	}
}

// Stop kills the unit Job Object so the whole process tree dies and
// cancels a pending Restart= relaunch. Stopping a target also stops its
// Wants=/Requires= in reverse After=/Before= order (DESIGN.md §42).
func (m *Manager) Stop(name string) (*protocol.UnitResult, error) {
	m.mu.Lock()
	ld, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	kind := ld.unit.Kind
	name = ld.unit.Name
	m.mu.Unlock()
	if kind == unit.KindTarget {
		return m.stopTransaction(name)
	}
	return m.stopUnit(name)
}

// Restart is stop then start.
func (m *Manager) Restart(ctx context.Context, name string) (*protocol.UnitResult, error) {
	if _, err := m.Stop(name); err != nil {
		return nil, err
	}
	return m.Start(ctx, name)
}

// Logs returns stored stdout/stderr for the unit. Follow is ignored
// (snapshot only; no streaming RPC).
func (m *Manager) Logs(p protocol.LogsParams) (*protocol.LogsResult, error) {
	m.mu.Lock()
	ld, err := m.lookup(p.Unit)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	name := ld.unit.Name
	js := m.journal
	m.mu.Unlock()

	entries := []protocol.LogEntry{}
	if js != nil {
		got, err := js.Read(name)
		if err != nil {
			return nil, protocol.ErrFailed(err.Error())
		}
		entries = make([]protocol.LogEntry, 0, len(got))
		for _, e := range got {
			le := protocol.LogEntry{
				Unit:         e.Unit,
				PID:          e.PID,
				Stream:       e.Stream,
				Message:      e.Message,
				InvocationID: e.InvocationID,
			}
			if !e.Timestamp.IsZero() {
				le.Timestamp = e.Timestamp.UTC().Format(time.RFC3339Nano)
			}
			entries = append(entries, le)
		}
	}
	return &protocol.LogsResult{Unit: name, Entries: entries}, nil
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
