package manager

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	cfg                  Config
	clk                  timers.Clock
	launch               runtime.Launcher
	journal              *journal.Store
	engine               *timers.Engine
	mu                   sync.Mutex
	configMu             sync.Mutex // serializes reload/enable/disable I/O and acceptance
	configNamespace      string
	configSequence       uint64
	configRevision       string
	units                map[string]*unitRuntime
	graph                *core.Graph
	closed               bool
	activeStarts         int
	activeStops          int
	activeOperations     map[string]*operationTask
	operationSequence    uint64
	operations           map[string]*protocol.OperationResult
	completedOperations  []string
	startFlights         map[string]*startFlight
	capacityWaiters      int
	startCapacityChanged chan struct{}
	scm                  runtime.SCM
	tasks                runtime.TaskScheduler
	regOpen              registry.OpenFunc
	evtOpen              eventlog.OpenFunc
	pathOpen             pathwatch.OpenFunc
	pathExistsOpen       pathwatch.OpenFunc
	pathExists           pathwatch.ExistsFunc
	session              sync.Mutex // serializes graphical-session.target start/stop
	ops                  unitOps    // per-unit start/stop/restart (issue #24)
	stops                stopSet
	closePending         []unitTeardown
}

// New creates a manager. Reload must be called to load units.
func New(cfg Config) (*Manager, error) {
	if cfg.OperationTimeout < 0 {
		return nil, fmt.Errorf("OperationTimeout must not be negative")
	}
	if cfg.MaxStartTransactions < 0 {
		return nil, fmt.Errorf("MaxStartTransactions must not be negative")
	}
	if cfg.MaxStopTransactions < 0 {
		return nil, fmt.Errorf("MaxStopTransactions must not be negative")
	}
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
		configNamespace: rand.Text(),
		cfg:             cfg,
		clk:             clk,
		launch:          launch,
		scm:             scm,
		tasks:           tasks,
		journal:         js,
		units:           make(map[string]*unitRuntime),
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
// is admitted after Close returns; an already accepted launch still owns its
// completion and cleanup. Shutdown drains those launches before Close.
func (m *Manager) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), defaultStopTimeout)
	defer cancel()
	if err := m.CloseContext(ctx); err != nil {
		log.Printf("winunitd: close manager: %v", err)
	}
}

// CloseContext bounds the wait for background controls and journal closure.
// A pending close keeps its handles and worker; retries join that same pass.
// Process shutdown remains the caller's responsibility through Shutdown.
func (m *Manager) CloseContext(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	m.closed = true
	m.cancelOperationsLocked()
	m.signalStartCapacityLocked()
	for _, rt := range m.units {
		if rt.startCancel != nil {
			rt.startCancel()
		}
		rt.cancelRestart()
	}
	m.mu.Unlock()
	return m.stops.wait(ctx, m.clock(), stopKey{manager: m}, 0, m.closePass)
}

func (m *Manager) closePass() error {
	m.mu.Lock()
	tds := m.closePending
	m.closePending = nil
	for _, rt := range m.units {
		tds = append(tds, rt.detachAsync())
	}
	m.mu.Unlock()
	for _, td := range tds {
		td.cancelNonblocking()
	}
	var result error
	var pending []unitTeardown
	for _, td := range tds {
		if err := td.closeBlocking(); err != nil {
			result = errors.Join(result, err)
			pending = append(pending, td)
		}
	}
	m.mu.Lock()
	m.closePending = append(m.closePending, pending...)
	m.mu.Unlock()
	if m.engine != nil {
		m.engine.Stop()
	}
	if m.journal != nil {
		result = errors.Join(result, m.journal.Close())
	}
	return result
}

// Handle implements protocol.Handler.
func (m *Manager) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case protocol.MethodOperation, protocol.MethodCancelOperation:
		var p protocol.OperationParams
		if err := protocol.DecodeParams(params, &p); err != nil {
			return nil, err
		}
		if method == protocol.MethodCancelOperation {
			return m.CancelOperation(ctx, p.ID)
		}
		return m.Operation(p.ID)
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
		return m.StopContext(ctx, p.Unit)
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
	procs := make([]runtime.Process, 0, len(names))
	scmNames := make([]string, 0, len(names))
	taskNames := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, m.unitStatusLocked(name))
		var u *unit.Unit
		var proc runtime.Process
		if rt := m.units[name]; rt != nil {
			u = rt.ownedUnit()
			proc = rt.proc
		}
		procs = append(procs, proc)
		scmNames = append(scmNames, scmServiceName(u))
		taskNames = append(taskNames, scheduledTaskName(u))
	}
	m.mu.Unlock()
	for i := range out {
		m.overlayProcessAndJournal(&out[i], procs[i])
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
			ConfigRevision: st.ConfigRevision,
			Name:           st.Name,
			Description:    st.Description,
			Path:           st.Path,
			LoadState:      st.LoadState,
			ActiveState:    st.ActiveState,
			Enabled:        st.Enabled,
			Unit:           activated,
		})
	}
	m.mu.Unlock()
	for i := range out {
		if m.engine == nil {
			continue
		}
		snapshot := m.engine.Status(out[i].Name)
		out[i].Next, out[i].Last = formatTimerStamp(snapshot.Next), formatTimerStamp(snapshot.Last)
		out[i].ArmedConfigRevision = snapshot.ConfigRevision
		if snapshot.Unit != "" {
			out[i].Unit = snapshot.Unit
		}
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
	proc := rt.proc
	svcName := scmServiceName(rt.ownedUnit())
	taskName := scheduledTaskName(rt.ownedUnit())
	m.mu.Unlock()
	m.overlayProcessAndJournal(&st, proc)
	m.overlaySCM(&st, svcName)
	m.overlayTask(&st, taskName)
	m.overlayTimer(&st)
	return &protocol.StatusResult{Unit: &st}, nil
}

func (m *Manager) machineLocked() *protocol.MachineStatus {
	ms := &protocol.MachineStatus{State: "running", ConfigRevision: m.configRevision}
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

// overlayProcessAndJournal observes only the captured process, never a later
// invocation. Native and journal locks must not run under the manager mutex.
// These observations are best-effort overlays, not an atomic aggregate snapshot.
func (m *Manager) overlayProcessAndJournal(st *protocol.UnitStatus, proc runtime.Process) {
	stats := m.journal.CaptureStats(st.Name)
	st.LogDroppedRecords = stats.DroppedRecords
	st.LogDroppedBytes = stats.DroppedBytes
	st.LogStorageErrors = stats.StorageErrors
	st.LogLastStorageError = stats.LastStorageError
	if proc != nil && proc.Alive() {
		st.MainPID = proc.PID()
	}
}

func (m *Manager) unitStatusLocked(name string) protocol.UnitStatus {
	rt := m.units[name]
	st := protocol.UnitStatus{
		Name:        name,
		LoadState:   "loaded",
		ActiveState: m.stateOfLocked(name).String(),
	}
	if rt != nil {
		st.SubState = rt.sub.String()
		st.TerminationUncertain = rt.stopUncertain
		st.LastOperationID = rt.lastOperationID
		st.ConfigRevision = rt.configRevision
		st.InvocationConfigRevision = rt.invocationRevision
		if rt.hub != nil {
			st.ArmedConfigRevision = rt.hub.revision
		}
		if rt.unavailable {
			st.LoadState = "unavailable"
		}
		st.Enabled = rt.enabled
		if rt.unit != nil {
			st.Description = rt.unit.Description
			st.Kind = string(rt.unit.Kind)
			st.Path = rt.unit.Path
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
		if u := rt.ownedUnit(); u != nil && u.Service != nil {
			svc := u.Service
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
	return m.startFromOrigin(ctx, name, nil)
}

func (m *Manager) startFromOrigin(ctx context.Context, name string, origin activationOrigin) (*protocol.UnitResult, error) {
	return m.startOperation(ctx, name, origin, false)
}

func (m *Manager) startOperation(ctx context.Context, name string, origin activationOrigin, restart bool) (*protocol.UnitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	name, err := requireUnit(name)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return nil, protocol.ErrFailed(err.Error())
	}
	if m.closed {
		m.mu.Unlock()
		return nil, protocol.ErrFailed("manager is shutting down or closed")
	}
	if origin != nil && !origin.validLocked(m) {
		m.mu.Unlock()
		return nil, protocol.ErrFailed("activation superseded")
	}
	g := m.graph
	if _, ok := m.units[name]; !ok {
		m.mu.Unlock()
		return nil, protocol.ErrNotFound(name)
	}
	if g == nil {
		m.mu.Unlock()
		return nil, protocol.ErrFailed("no units loaded")
	}
	if origin == nil && !restart {
		if flight := m.startFlights[name]; flight != nil && flight.record == m.units[name] && flight.stopEpoch == flight.record.stopEpoch && flight.revision == m.configRevision {
			m.mu.Unlock()
			return flight.wait(ctx)
		}
	}
	limit := m.cfg.MaxStartTransactions
	if limit == 0 {
		limit = DefaultMaxStartTransactions
	}
	if m.activeStarts >= limit {
		m.mu.Unlock()
		return nil, errStartCapacity
	}
	roots := []string{name}
	var stopPlan *core.Transaction
	if restart {
		var intent *restartOrigin
		stopPlan, roots, intent, err = m.planRestartLocked(g, name)
		if err != nil {
			m.mu.Unlock()
			return nil, protocol.ErrFailed(waitFailMessage(err))
		}
		origin = intent
	}
	tx, err := g.PlanStart(roots...)
	if err != nil {
		m.mu.Unlock()
		return nil, protocol.ErrFailed(waitFailMessage(err))
	}
	m.activeStarts++
	if stopPlan != nil {
		m.disarmStopScopeLocked(stopPlan)
	}
	definitions := make(map[string]*plannedStart)
	for _, member := range tx.Units() {
		if rt := m.units[member]; rt != nil {
			definitions[member] = &plannedStart{unit: rt.unit, revision: rt.configRevision, record: rt, stopEpoch: rt.stopEpoch, gen: rt.gen, origin: origin}
			if intent, ok := origin.(*restartOrigin); ok {
				if stopped, ok := intent.members[member]; ok {
					definitions[member].stopEpoch = stopped.stopEpoch
					definitions[member].gen++ // own stop increments the generation
				}
			}
			rt.operations++
		}
	}
	// Stop-only members also remain owned across reload and delayed teardown.
	var retainedStops []*unitRuntime
	if intent, ok := origin.(*restartOrigin); ok {
		for _, member := range intent.members {
			member.record.operations++
			retainedStops = append(retainedStops, member.record)
		}
	}
	action := protocol.MethodStart
	if restart {
		action = protocol.MethodRestart
	}
	members := tx.Units()
	if stopPlan != nil {
		members = append(members, stopPlan.Units()...)
	}
	operationID := m.beginOperationLocked(name, action, origin, members)
	flight := &startFlight{id: operationID, record: m.units[name], stopEpoch: m.units[name].stopEpoch, revision: m.configRevision, done: make(chan struct{})}
	if origin == nil && !restart {
		if m.startFlights == nil {
			m.startFlights = make(map[string]*startFlight)
		}
		m.startFlights[name] = flight
	}
	task := m.beginOperationTaskLocked(flight, m.operationTimeoutLocked(tx, stopPlan), definitions)
	m.mu.Unlock()
	go func() {
		result, err := m.executeStartOperation(task.ctx, name, origin, tx, stopPlan, definitions)
		m.finishOperationTask(name, task, result, err, func() {
			m.activeStarts--
			m.signalStartCapacityLocked()
			for _, planned := range definitions {
				planned.record.operations--
			}
			for _, record := range retainedStops {
				record.operations--
			}
		})
	}()
	return flight.wait(ctx)
}

func (m *Manager) executeStartOperation(ctx context.Context, name string, origin activationOrigin, tx, stopPlan *core.Transaction, definitions map[string]*plannedStart) (*protocol.UnitResult, error) {
	if stopPlan != nil {
		if _, err := stopPlan.ExecuteStop(ctx, core.StopFunc(m.stopAcceptedUnitCtx)); err != nil {
			return nil, protocol.ErrFailed(waitFailMessage(err))
		}
	}
	_, err := tx.Execute(ctx, operationStarter{manager: m, definitions: definitions})
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		return &protocol.UnitResult{
			Unit:        name,
			ActiveState: m.stateOfLocked(name).String(),
			Error:       waitFailMessage(err),
		}, protocol.ErrFailed(waitFailMessage(err))
	}
	return &protocol.UnitResult{Unit: name, ActiveState: m.stateOfLocked(name).String()}, nil
}

// Stop stops the named unit plus reverse requirers (units that
// Requires=/BindsTo=/PartOf= it), dependents first. Forward Requires=/Wants=
// and pure After=/Before= neighbors are not stopped (DESIGN.md §10, §42).
// Manager.Shutdown is a separate plan (full reverse After=/Before=).
func (m *Manager) Stop(name string) (*protocol.UnitResult, error) {
	return m.StopContext(context.Background(), name)
}

// StopContext cancels only this caller's wait after admission.
func (m *Manager) StopContext(ctx context.Context, name string) (*protocol.UnitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	m.mu.Lock()
	rt, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	name = rt.unit.Name
	m.mu.Unlock()
	return m.stopTransaction(ctx, name)
}

// Restart admits captured stop/start plans as one operation.
func (m *Manager) Restart(ctx context.Context, name string) (*protocol.UnitResult, error) {
	return m.startOperation(ctx, name, nil, true)
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

	more := false
	collect := func() ([]protocol.LogEntry, string, error) {
		if js == nil {
			return []protocol.LogEntry{}, p.Cursor, nil
		}
		// Entry JSON is a conservative bound for LogEntry (which omits empty
		// fields). Reserve space below the 1 MiB RPC limit for the envelope.
		got, cursor, hasMore, err := js.QueryPage(name, since, p.Cursor, (1<<20)-4096)
		more = hasMore
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
				Continuation: e.Continuation,
				Partial:      e.Partial,
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
	return &protocol.LogsResult{Unit: name, Entries: entries, Cursor: cursor, More: more}, nil
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
