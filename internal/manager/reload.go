package manager

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
)

// Reload reparses unit files, rebuilds the graph, and preserves process
// instances by keeping the whole unitRuntime for units that remain
// (DESIGN.md §33). Enable files under enabled/<target>/<unit> become
// extra Wants= on those targets. Parse and directory reads happen
// outside m.mu; the map swap is under the lock (issue #26).
func (m *Manager) Reload() (*protocol.DaemonReloadResult, error) {
	loaded, result, err := m.parseUnitDir()
	if err != nil {
		return nil, err
	}

	loaded = mergeBuiltins(loaded, m.cfg.UserScope)
	links := m.readEnabledLinks()
	graphUnits := withEnabledWants(loaded, links)
	g, err := core.Build(graphUnits)
	if err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	if c := g.OrderingCycle(); c != nil {
		result.Cycle = c.Error()
	}

	m.mu.Lock()
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
		rt.enabled = len(targets) > 0
		rt.targets = targets
		next[name] = rt
	}
	var dropped []unitTeardown
	for name, rt := range m.units {
		if keep[name] {
			continue
		}
		td := rt.detachAsync()
		td.cancelNonblocking()
		dropped = append(dropped, td)
	}
	m.units = next
	m.graph = g
	// Running jobs stay on the kept unitRuntime. Vanished units are not
	// stopped (DESIGN.md §33); their restart timers are cancelled above
	// so a mid-delay drop cannot relaunch or leave SubAutoRestart.
	m.syncTimersLocked()
	m.syncRegistryLocked()
	m.syncEventLogLocked()
	return dropped
}
