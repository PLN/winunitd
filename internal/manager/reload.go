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
// instances by keeping states (and running jobs) for units that remain
// (DESIGN.md §33). Enable files under enabled/<target>/<unit> become
// extra Wants= on those targets.
func (m *Manager) Reload() (*protocol.DaemonReloadResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	unitsPath := m.cfg.UnitsDir()
	result := &protocol.DaemonReloadResult{}

	entries, err := os.ReadDir(unitsPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, protocol.ErrFailed(err.Error())
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
		scopeFail := false
		for _, iss := range unit.RegistryScopeIssues(rep.Unit, m.cfg.UserScope) {
			result.Errors = append(result.Errors, iss.String())
			scopeFail = true
		}
		if scopeFail {
			continue
		}
		loaded = append(loaded, rep.Unit)
	}

	loaded = mergeBuiltins(loaded, m.cfg.UserScope)
	graphUnits := withEnabledWants(loaded, m.readEnabledLinks())
	g, err := core.Build(graphUnits)
	if err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	if c := g.OrderingCycle(); c != nil {
		result.Cycle = c.Error()
	}

	m.replaceLocked(loaded, g)
	result.Loaded = len(loaded)
	return result, nil
}

func (m *Manager) replaceLocked(units []*unit.Unit, g *core.Graph) {
	oldStates := m.states
	oldErrors := m.errors
	m.units = make(map[string]*loaded, len(units))
	m.states = make(map[string]core.State, len(units))
	m.errors = make(map[string]string)
	m.graph = g
	for _, u := range units {
		name := core.NormalizeName(u.Name)
		targets := m.enabledTargets(name)
		m.units[name] = &loaded{
			unit:    u,
			enabled: len(targets) > 0,
			targets: targets,
		}
		if s, ok := oldStates[name]; ok {
			m.states[name] = s
		} else {
			m.states[name] = core.Inactive
		}
		if err, ok := oldErrors[name]; ok {
			m.errors[name] = err
		}
	}
	// Running jobs (procs, gens, subs, cancels) stay on the manager for
	// units that remain. Vanished units are not stopped (DESIGN.md §33).
	m.syncTimersLocked()
	m.syncRegistryLocked()
}
