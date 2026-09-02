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
	"github.com/PLN/winunitd/internal/eventlog"
	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/pathwatch"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/registry"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
	"github.com/PLN/winunitd/internal/unit"
)

// Manager holds loaded units and serves the control protocol.
type Manager struct {
	cfg            Config
	clk            timers.Clock
	launch         runtime.Launcher
	journal        *journal.Store
	engine         *timers.Engine
	mu             sync.Mutex
	units          map[string]*unitRuntime
	graph          *core.Graph
	closed         bool
	scm            runtime.SCM
	tasks          runtime.TaskScheduler
	regOpen        registry.OpenFunc
	evtOpen        eventlog.OpenFunc
	pathOpen       pathwatch.OpenFunc
	pathExistsOpen pathwatch.OpenFunc
	pathExists     pathwatch.ExistsFunc
	session        sync.Mutex // serializes graphical-session.target start/stop
	ops            unitOps    // per-unit start/stop/restart (issue #24)
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
	scm := cfg.SCM
	if scm == nil {
		scm = runtime.DefaultSCM()
	}
	tasks := cfg.Tasks
	if tasks == nil {
		tasks = runtime.DefaultTaskScheduler()
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
	if clk.Now == nil || clk.SinceBoot == nil || clk.Startup.IsZero() || clk.NewTimer == nil || clk.SinceStart == nil {
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
		if clk.NewTimer == nil {
			clk.NewTimer = def.NewTimer
		}
		if clk.SinceStart == nil {
			clk.SinceStart = def.SinceStart
		}
	}
	m := &Manager{
		cfg:     cfg,
		clk:     clk,
		launch:  launch,
		scm:     scm,
		tasks:   tasks,
		journal: js,
		units:   make(map[string]*unitRuntime),
	}
	if cfg.RegistryOpen != nil {
		m.regOpen = cfg.RegistryOpen
	} else {
		m.regOpen = registry.OpenWatch
	}
	if cfg.EventLogOpen != nil {
		m.evtOpen = cfg.EventLogOpen
	} else {
		m.evtOpen = eventlog.OpenSubscribe
	}
	if cfg.PathOpen != nil {
		m.pathOpen = cfg.PathOpen
	} else {
		m.pathOpen = pathwatch.OpenWatch
	}
	if cfg.PathExistsOpen != nil {
		m.pathExistsOpen = cfg.PathExistsOpen
	} else {
		m.pathExistsOpen = pathwatch.OpenExistsWatch
	}
	if cfg.PathExists != nil {
		m.pathExists = cfg.PathExists
	} else {
		m.pathExists = pathwatch.Exists
	}
	m.engine = timers.NewEngine(clk, store, m.onTimerElapsed)
	return m, nil
}

// Close stops the timer scheduler, notify listeners, watchdogs,
// registry/eventlog/path watches, and pending Restart= timers. No relaunch
// runs after Close returns; Shutdown is not required first (issue #26).
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	tds := make([]unitTeardown, 0, len(m.units))
	for _, rt := range m.units {
		tds = append(tds, rt.detachAsync())
	}
	m.mu.Unlock()
	for _, td := range tds {
		td.cancelNonblocking()
	}
	for _, td := range tds {
		td.closeBlocking()
	}
	if m.engine != nil {
		m.engine.Stop()
	}
	if m.journal != nil {
		_ = m.journal.Close()
	}
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

func (m *Manager) lookup(name string) (*unitRuntime, error) {
	name, err := requireUnit(name)
	if err != nil {
		return nil, err
	}
	rt, ok := m.units[name]
	if !ok || rt == nil {
		return nil, protocol.ErrNotFound(name)
	}
	return rt, nil
}

// ListUnits returns loaded units in name order.
func (m *Manager) ListUnits() (*protocol.ListUnitsResult, error) {
	m.mu.Lock()
	names := m.names()
	out := make([]protocol.UnitStatus, 0, len(names))
	scmNames := make([]string, 0, len(names))
	taskNames := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, m.unitStatusLocked(name))
		var u *unit.Unit
		if rt := m.units[name]; rt != nil {
			u = rt.unit
		}
		scmNames = append(scmNames, scmServiceName(u))
		taskNames = append(taskNames, scheduledTaskName(u))
	}
	m.mu.Unlock()
	for i := range out {
		m.overlaySCM(&out[i], scmNames[i])
		m.overlayTask(&out[i], taskNames[i])
		m.overlayTimer(&out[i])
	}
	return &protocol.ListUnitsResult{Units: out}, nil
}

// ListTimers returns loaded timer units.
func (m *Manager) ListTimers() (*protocol.ListTimersResult, error) {
	m.mu.Lock()
	var out []protocol.TimerStatus
	for _, name := range m.names() {
		rt := m.units[name]
		if rt == nil || rt.unit == nil || rt.unit.Kind != unit.KindTimer {
			continue
		}
		st := m.unitStatusLocked(name)
		activated := ""
		if rt.unit.Timer != nil {
			activated = rt.unit.Timer.Unit
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
	m.mu.Unlock()
	for i := range out {
		out[i].Next, out[i].Last = m.timerStamps(out[i].Name)
	}
	return &protocol.ListTimersResult{Timers: out}, nil
}

// Status returns machine status or a single unit.
func (m *Manager) Status(name string) (*protocol.StatusResult, error) {
	m.mu.Lock()
	if strings.TrimSpace(name) == "" {
		ms := m.machineLocked()
		m.mu.Unlock()
		return &protocol.StatusResult{Machine: ms}, nil
	}
	rt, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	st := m.unitStatusLocked(rt.unit.Name)
	svcName := scmServiceName(rt.unit)
	taskName := scheduledTaskName(rt.unit)
	m.mu.Unlock()
	m.overlaySCM(&st, svcName)
	m.overlayTask(&st, taskName)
	m.overlayTimer(&st)
	return &protocol.StatusResult{Unit: &st}, nil
}

func (m *Manager) machineLocked() *protocol.MachineStatus {
	ms := &protocol.MachineStatus{State: "running"}
	for name, rt := range m.units {
		ms.UnitsLoaded++
		if rt != nil && rt.unit != nil && rt.unit.Kind == unit.KindTimer {
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
	rt := m.units[name]
	st := protocol.UnitStatus{
		Name:        name,
		LoadState:   "loaded",
		ActiveState: m.stateOfLocked(name).String(),
	}
	if rt != nil {
		st.Enabled = rt.enabled
		if rt.unit != nil {
			st.Description = rt.unit.Description
			st.Kind = string(rt.unit.Kind)
			st.Path = rt.unit.Path
		}
		if rt.proc != nil && rt.proc.Alive() {
			st.MainPID = rt.proc.PID()
		}
		if rt.invocation != "" {
			st.InvocationID = rt.invocation
		}
		if rt.err != "" {
			st.Error = rt.err
			if r := core.StatusReason(rt.err); r != "" {
				st.Reason = r
			}
		}
		if rt.unit != nil && rt.unit.Service != nil {
			svc := rt.unit.Service
			if svc.CPUWeightSet {
				st.CPUWeight = svc.CPUWeight
			}
			if svc.CPUQuotaSet {
				st.CPUQuota = svc.CPUQuota
			}
			if svc.IoPrioritySet {
				st.IoPriority = string(svc.IoPriority)
			}
		}
	}
	return st
}

func (m *Manager) stateOfLocked(name string) core.State {
	if rt := m.units[name]; rt != nil {
		return rt.state
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
		m.setErrLocked(name, waitFailMessage(err))
		return &protocol.UnitResult{
			Unit:        name,
			ActiveState: m.stateOfLocked(name).String(),
			Error:       waitFailMessage(err),
		}, protocol.ErrFailed(waitFailMessage(err))
	}
	m.clearErrLocked(name)
	return &protocol.UnitResult{Unit: name, ActiveState: m.stateOfLocked(name).String()}, nil
}

func (m *Manager) applyRunLocked(run *core.Run) {
	if run == nil {
		return
	}
	for name, st := range run.States {
		rt := m.units[name]
		if rt == nil || rt.sub == core.SubAutoRestart {
			continue
		}
		// Do not stamp Failed over an already-Active unit (Requires=
		// failure of a dependency after this unit started). Issue #35.
		if st == core.Failed && rt.state == core.Active {
			continue
		}
		// Do not stamp Active onto a unit stopped mid-transaction (#24).
		if st == core.Active && rt.stopping {
			continue
		}
		// Do not stamp Inactive over a unit that started after this run's
		// stop (stop-transaction apply vs a later Start).
		if st == core.Inactive && !rt.stopping {
			if rt.state == core.Active || rt.state == core.Activating {
				continue
			}
			if live := rt.proc; live != nil && live.Alive() {
				continue
			}
		}
		rt.state = st
	}
	for name, err := range run.Errors {
		rt := m.units[name]
		if rt == nil || err == nil {
			continue
		}
		if rt.state == core.Active {
			continue
		}
		rt.err = waitFailMessage(err)
	}
}

// Stop stops the named unit plus reverse requirers (units that
// Requires=/BindsTo=/PartOf= it), dependents first. Forward Requires=/Wants=
// and pure After=/Before= neighbors are not stopped (DESIGN.md §10, §42).
// Manager.Shutdown is a separate plan (full reverse After=/Before=).
func (m *Manager) Stop(name string) (*protocol.UnitResult, error) {
	m.mu.Lock()
	rt, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	name = rt.unit.Name
	m.mu.Unlock()
	return m.stopTransaction(name)
}

// Restart is stop then start.
func (m *Manager) Restart(ctx context.Context, name string) (*protocol.UnitResult, error) {
	if _, err := m.Stop(name); err != nil {
		return nil, err
	}
	return m.Start(ctx, name)
}

// Logs returns stored stdout/stderr for the unit. Since is a lower bound
// (invalid values are invalid-params, not ignored). Follow waits briefly
// for new lines after Cursor; the client polls with the returned cursor.
func (m *Manager) Logs(p protocol.LogsParams) (*protocol.LogsResult, error) {
	m.mu.Lock()
	rt, err := m.lookup(p.Unit)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	name := rt.unit.Name
	js := m.journal
	m.mu.Unlock()
	now := m.now()

	var since time.Time
	if strings.TrimSpace(p.Since) != "" {
		t, err := journal.ParseSince(p.Since, now)
		if err != nil {
			return nil, protocol.ErrInvalidParams(err.Error())
		}
		since = t
	}

	collect := func() ([]protocol.LogEntry, string, error) {
		if js == nil {
			return []protocol.LogEntry{}, p.Cursor, nil
		}
		got, cursor, err := js.Query(name, since, p.Cursor)
		if err != nil {
			return nil, cursor, err
		}
		entries := make([]protocol.LogEntry, 0, len(got))
		for _, e := range got {
			le := protocol.LogEntry{
				Unit:         e.Unit,
				PID:          e.PID,
				Stream:       e.Stream,
				Message:      e.Message,
				InvocationID: e.InvocationID,
				Severity:     e.Severity,
				Session:      e.Session,
				UserSID:      e.UserSID,
			}
			if !e.Timestamp.IsZero() {
				le.Timestamp = e.Timestamp.UTC().Format(time.RFC3339Nano)
			}
			entries = append(entries, le)
		}
		return entries, cursor, nil
	}

	entries, cursor, err := collect()
	if err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	if p.Follow && len(entries) == 0 {
		deadline := m.now().Add(journal.FollowWait())
		for len(entries) == 0 && m.now().Before(deadline) {
			wait := journal.FollowPoll()
			if rem := deadline.Sub(m.now()); wait > rem {
				wait = rem
			}
			if wait > 0 {
				tmr := m.clock().Timer(wait)
				<-tmr.C()
				tmr.Stop()
			}
			entries, cursor, err = collect()
			if err != nil {
				return nil, protocol.ErrFailed(err.Error())
			}
		}
	}
	if entries == nil {
		entries = []protocol.LogEntry{}
	}
	return &protocol.LogsResult{Unit: name, Entries: entries, Cursor: cursor}, nil
}

// Verify re-reads a loaded unit file (daemon-side; path verify stays in winctl).
func (m *Manager) Verify(name string) (*protocol.VerifyResult, error) {
	m.mu.Lock()
	rt, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	path := rt.unit.Path
	unitName := rt.unit.Name
	kind := rt.unit.Kind
	m.mu.Unlock()

	rep := unit.VerifyPath(path)
	m.mu.Lock()
	userScope := m.cfg.UserScope
	var companionMissing string
	if kind == unit.KindRegistry || kind == unit.KindEventLog || kind == unit.KindPath {
		companion := unit.CompanionService(unitName)
		if _, ok := m.units[companion]; !ok {
			companionMissing = companion
		}
	}
	m.mu.Unlock()

	rep.Issues = append(rep.Issues, unit.RegistryScopeIssues(rep.Unit, userScope)...)
	rep.Issues = append(rep.Issues, unit.EventLogScopeIssues(rep.Unit, userScope)...)
	if companionMissing != "" {
		rep.Issues = append(rep.Issues, unit.Issue{
			Path:     path,
			Severity: unit.SeverityError,
			Message:  "missing companion " + companionMissing,
		})
	}
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
