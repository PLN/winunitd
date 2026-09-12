package manager

import (
	"errors"
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
// graph construction uses a captured retention set outside m.mu. Publication
// validates that set before swapping the graph and runtime records.
func (m *Manager) Reload() (*protocol.DaemonReloadResult, error) {
	return m.reloadWithBuilder(core.Build)
}

// The builder is pure graph planning; it must not mutate accepted definitions.
func (m *Manager) reloadWithBuilder(build func([]*unit.Unit) (*core.Graph, error)) (*protocol.DaemonReloadResult, error) {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	finish, err := m.beginConfigWork()
	if err != nil {
		return nil, err
	}
	defer finish()
	loaded, result, err := m.parseUnitDir()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	result.ConfigRevision = m.configRevision
	m.mu.Unlock()
	// A candidate is accepted as a whole. An invalid file is not a removal,
	// and valid neighbours must not become visible from a rejected directory.
	if len(result.Errors) != 0 {
		return result, nil
	}

	loaded = mergeBuiltins(loaded, m.cfg.UserScope)
	links, err := m.readEnabledLinks()
	if err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	accepted := make(map[string]bool, len(loaded))
	for _, u := range loaded {
		accepted[core.NormalizeName(u.Name)] = true
	}
	// Lifecycle work continues during planning. Retry a bounded number of times
	// if ownership changed; never publish a graph missing newly retained units.
	for attempt := 0; attempt < 3; attempt++ {
		m.mu.Lock()
		snapshot := m.captureReloadOwnershipLocked(accepted)
		m.mu.Unlock()
		graphUnits := append([]*unit.Unit(nil), loaded...)
		for _, u := range snapshot.retained {
			graphUnits = append(graphUnits, u)
		}
		g, buildErr := build(withEnabledWants(graphUnits, links))
		cycle := ""
		if buildErr == nil {
			if c := g.OrderingCycle(); c != nil {
				cycle = c.Error()
			}
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, protocol.ErrFailed("manager is shutting down or closed")
		}
		if !m.reloadOwnershipCurrentLocked(accepted, snapshot) {
			m.mu.Unlock()
			continue
		}
		if buildErr != nil {
			m.mu.Unlock()
			return nil, protocol.ErrFailed(buildErr.Error())
		}
		if cycle != "" {
			result.Cycle = cycle
			m.mu.Unlock()
			return result, nil
		}
		dropped := m.replaceLocked(loaded, g, links)
		result.ConfigRevision = m.configRevision
		m.mu.Unlock()
		var cleanupErr error
		for _, td := range dropped {
			if err := td.closeBlocking(); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
				m.mu.Lock()
				m.closePending = append(m.closePending, td)
				m.mu.Unlock()
			}
		}
		result.Loaded = len(loaded)
		if cleanupErr != nil {
			return result, protocol.ErrFailed(cleanupErr.Error())
		}
		return result, nil
	}
	return nil, &protocol.Error{Code: protocol.CodeBusy, Message: "configuration ownership changed during reload; retry"}
}

func (m *Manager) parseUnitDir() ([]*unit.Unit, *protocol.DaemonReloadResult, error) {
	unitsPath := m.cfg.UnitsDir()
	result := &protocol.DaemonReloadResult{}

	entries, err := readOptionalDirectory(unitsPath)
	if err != nil {
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

// A genuinely absent directory is an empty candidate. On Windows ReadDir on
// an existing regular file can also report a not-found error; do not mistake
// that unreadable configuration source for a valid removal of every entry.
func readOptionalDirectory(path string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return nil, nil
		}
	}
	return entries, err
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
