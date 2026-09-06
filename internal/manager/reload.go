package manager

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
)

// Reload reparses unit files and rebuilds the graph. Live processes and
// in-flight lifecycle operations retain their runtime record even if the
// configuration disappears. Parse and directory reads happen outside m.mu;
// graph construction and the runtime map swap share the lock.
func (m *Manager) Reload() (*protocol.DaemonReloadResult, error) {
	loaded, result, err := m.parseUnitDir()
	if err != nil {
		return nil, err
	}

	loaded = mergeBuiltins(loaded, m.cfg.UserScope)
	links := m.readEnabledLinks()
	m.mu.Lock()
	// Retained configurations keep stop/dependency planning possible even when
	// the latest directory no longer supplies a valid unit. Start admission is
	// checked against the runtime record, not merely graph membership.
	graphUnits := append([]*unit.Unit(nil), loaded...)
	accepted := make(map[string]bool, len(loaded))
	for _, u := range loaded {
		accepted[core.NormalizeName(u.Name)] = true
	}
	for name, rt := range m.units {
		if !accepted[name] && rt.retainWithoutConfig() {
			graphUnits = append(graphUnits, rt.unit)
		}
	}
	graphUnits = withEnabledWants(graphUnits, links)
	g, err := core.Build(graphUnits)
	if err != nil {
		m.mu.Unlock()
		return nil, protocol.ErrFailed(err.Error())
	}
	if c := g.OrderingCycle(); c != nil {
		result.Cycle = c.Error()
	}

	dropped := m.replaceLocked(loaded, g, links)
	m.mu.Unlock()
	for _, td := range dropped {
		td.closeBlocking()
	}

	result.Loaded = len(loaded)
	return result, nil
}

func (m *Manager) parseUnitDir() ([]*unit.Unit, *protocol.DaemonReloadResult, error) {
	unitsPath := m.cfg.UnitsDir()
	result := &protocol.DaemonReloadResult{}

	entries, err := os.ReadDir(unitsPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, protocol.ErrFailed(err.Error())
	}

	var loaded []*unit.Unit
	seen := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if _, kerr := unit.KindFromName(name); kerr != nil {
			continue
		}
		path := filepath.Join(unitsPath, name)
		rep := unit.VerifyPath(path)
		norm := core.NormalizeName(name)
		if prev, ok := seen[norm]; ok {
			result.Errors = append(result.Errors, fmt.Sprintf("duplicate unit %q (%s and %s)", norm, prev, path))
			continue
		}
		seen[norm] = path
		if rep.HasError() {
			for _, iss := range rep.Errors() {
				result.Errors = append(result.Errors, iss.String())
			}
			continue
		}
		if rep.Unit == nil {
			result.Errors = append(result.Errors, path+": parse produced no unit")
			continue
		}
		if m.cfg.UserScope && scmServiceName(rep.Unit) != "" {
			result.Errors = append(result.Errors, path+": Type=scm is only supported in the system manager")
			continue
		}
		if m.cfg.UserScope && scheduledTaskName(rep.Unit) != "" {
			result.Errors = append(result.Errors, path+": Type=scheduled-task is only supported in the system manager")
			continue
		}
		scopeFail := false
		for _, iss := range unit.RegistryScopeIssues(rep.Unit, m.cfg.UserScope) {
			result.Errors = append(result.Errors, iss.String())
			scopeFail = true
		}
		for _, iss := range unit.EventLogScopeIssues(rep.Unit, m.cfg.UserScope) {
			result.Errors = append(result.Errors, iss.String())
			scopeFail = true
		}
		if scopeFail {
			continue
		}
		loaded = append(loaded, rep.Unit)
	}
	return loaded, result, nil
}

func (m *Manager) replaceLocked(units []*unit.Unit, g *core.Graph, links map[string][]string) []unitTeardown {
	next := make(map[string]*unitRuntime, len(units))
	keep := make(map[string]bool, len(units))
	for _, u := range units {
		name := core.NormalizeName(u.Name)
		keep[name] = true
		rt := m.units[name]
		if rt == nil {
			rt = &unitRuntime{state: core.Inactive}
		}
		targets := enabledTargetsFrom(links, name)
		rt.unit = u
		rt.unavailable = false
		rt.enabled = len(targets) > 0
		rt.targets = targets
		next[name] = rt
	}
	var dropped []unitTeardown
	for name, rt := range m.units {
		if keep[name] {
			continue
		}
		if rt.retainWithoutConfig() {
			rt.unavailable = true
			rt.enabled = false
			rt.targets = nil
			rt.cancelRestart()
			next[name] = rt
			continue
		}
		td := rt.detachAsync()
		td.cancelNonblocking()
		dropped = append(dropped, td)
	}
	m.units = next
	m.signalStartCapacityLocked()
	m.graph = g
	// Live and in-flight records survive independently of configuration files.
	// Unowned vanished units are dropped and their async controls cancelled.
	m.syncTimersLocked()
	m.syncHubsLocked()
	return dropped
}

// retainWithoutConfig keeps stop routing available until ownership is resolved.
// Native proxies have no process handle; their last configuration still names
// the external service/task that Stop must address. Failed native starts can
// also leave an uncertain external outcome, so only Inactive permits removal.
// Caller holds m.mu.
func (rt *unitRuntime) retainWithoutConfig() bool {
	if rt.proc != nil || rt.notify != nil || rt.hub != nil || rt.operations != 0 || rt.stopUncertain {
		return true
	}
	native := scmServiceName(rt.ownedUnit()) != "" || scheduledTaskName(rt.ownedUnit()) != ""
	return native && rt.state != core.Inactive
}
