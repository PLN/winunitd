package manager

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
)

// Enable writes tiny link files under enabled/<target>/ (DESIGN.md §12).
// Enable does not start the unit. An empty WantedBy= means default.target.
// Directory writes happen outside m.mu; the graph swap is under the lock.
func (m *Manager) Enable(name string) (*protocol.EnableResult, error) {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	finish, err := m.beginConfigWork()
	if err != nil {
		return nil, err
	}
	defer finish()
	m.mu.Lock()
	rt, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	name = core.NormalizeName(rt.unit.Name)
	targets := append([]string(nil), rt.unit.WantedBy...)
	m.mu.Unlock()

	if len(targets) == 0 {
		targets = []string{DefaultTarget}
	}
	normalized := uniqueTargets(targets)
	for _, t := range normalized {
		path := m.cfg.EnabledPath(t, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, protocol.ErrFailed(err.Error())
		}
		if err := os.WriteFile(path, []byte(name+"\n"), 0o644); err != nil {
			return nil, protocol.ErrFailed(err.Error())
		}
	}
	links, err := m.readEnabledLinks()
	if err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}

	if err := m.rebuildGraphWithLinks(links, core.Build); err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, err = m.lookup(name)
	if err != nil {
		return nil, err
	}
	return &protocol.EnableResult{Unit: name, Enabled: true, Targets: normalized}, nil
}

// Disable removes enable files for the unit.
func (m *Manager) Disable(name string) (*protocol.EnableResult, error) {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	finish, err := m.beginConfigWork()
	if err != nil {
		return nil, err
	}
	defer finish()
	m.mu.Lock()
	rt, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	name = core.NormalizeName(rt.unit.Name)
	m.mu.Unlock()

	// Enumerate the same bounded namespace as reload before removing anything.
	// Nested directories are not enable records and must not be traversed.
	files, err := m.readEnabledFiles()
	if err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	for _, path := range files {
		if core.NormalizeName(filepath.Base(path)) == name {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return nil, protocol.ErrFailed(err.Error())
			}
		}
	}
	links, err := m.readEnabledLinks()
	if err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}

	if err := m.rebuildGraphWithLinks(links, core.Build); err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, err = m.lookup(name)
	if err != nil {
		return nil, err
	}
	return &protocol.EnableResult{Unit: name, Enabled: rt.enabled, Targets: rt.targets}, nil
}

func uniqueTargets(targets []string) []string {
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
	return normalized
}

func enabledTargetsFrom(links map[string][]string, name string) []string {
	var targets []string
	for target, units := range links {
		for _, u := range units {
			if u == name {
				targets = append(targets, target)
				break
			}
		}
	}
	sort.Strings(targets)
	return targets
}

// readEnabledLinks maps target name -> enabled unit names from
// <base-dir>/enabled/<target>/<unit> files (not NTFS symlinks).
func (m *Manager) readEnabledLinks() (map[string][]string, error) {
	files, err := m.readEnabledFiles()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string)
	for _, path := range files {
		name := core.NormalizeName(filepath.Base(path))
		if _, err := unit.KindFromName(name); err != nil {
			continue
		}
		target := core.NormalizeName(filepath.Base(filepath.Dir(path)))
		out[target] = append(out[target], name)
	}
	for target := range out {
		sort.Strings(out[target])
	}
	return out, nil
}

// The root and immediate target entries share one budget. Return no partial
// candidate when enumeration fails, including before Disable mutates links.
func (m *Manager) readEnabledFiles() ([]string, error) {
	dir := m.cfg.EnabledDir()
	ents, err := readOptionalDirectory(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	remaining := maxConfigurationEntries - len(ents)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		files, err := readConfigurationDirectory(filepath.Join(dir, e.Name()), remaining)
		if err != nil {
			return nil, err
		}
		remaining -= len(files)
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			out = append(out, filepath.Join(dir, e.Name(), f.Name()))
		}
	}
	return out, nil
}

// withEnabledWants returns units for graph build: each target's Wants=
// includes units enabled into it (DESIGN.md §12: default.target wants hermes).
func withEnabledWants(units []*unit.Unit, enabled map[string][]string) []*unit.Unit {
	if len(enabled) == 0 {
		return units
	}
	loaded := make(map[string]struct{}, len(units))
	for _, u := range units {
		loaded[core.NormalizeName(u.Name)] = struct{}{}
	}
	out := make([]*unit.Unit, len(units))
	for i, u := range units {
		extra := enabled[core.NormalizeName(u.Name)]
		if len(extra) == 0 {
			out[i] = u
			continue
		}
		c := *u
		wants := append([]string(nil), u.Wants...)
		for _, name := range extra {
			if _, ok := loaded[name]; !ok {
				continue
			}
			wants = append(wants, name)
		}
		c.Wants = wants
		out[i] = &c
	}
	return out
}

func (m *Manager) parsedUnitsLocked() []*unit.Unit {
	out := make([]*unit.Unit, 0, len(m.units))
	for _, name := range m.names() {
		if rt := m.units[name]; rt != nil && rt.unit != nil {
			out = append(out, rt.unit)
		}
	}
	return out
}

// Caller holds configMu so accepted definitions and links cannot change while
// graph planning runs. Lifecycle state may change and is not a graph input here.
func (m *Manager) rebuildGraphWithLinks(links map[string][]string, build func([]*unit.Unit) (*core.Graph, error)) error {
	m.mu.Lock()
	parsed := m.parsedUnitsLocked()
	m.mu.Unlock()
	g, err := build(withEnabledWants(parsed, links))
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return protocol.ErrFailed("manager is shutting down or closed")
	}
	m.acceptEnabledGraphLocked(g, links)
	return nil
}
