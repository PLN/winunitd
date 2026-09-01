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
	links := m.readEnabledLinks()

	m.mu.Lock()
	defer m.mu.Unlock()
	rt, err = m.lookup(name)
	if err != nil {
		return nil, err
	}
	if err := m.rebuildGraphWithLinksLocked(links); err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	rt.enabled = true
	rt.targets = normalized
	return &protocol.EnableResult{Unit: name, Enabled: true, Targets: normalized}, nil
}

// Disable removes enable files for the unit.
func (m *Manager) Disable(name string) (*protocol.EnableResult, error) {
	m.mu.Lock()
	rt, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	name = core.NormalizeName(rt.unit.Name)
	m.mu.Unlock()

	_ = filepath.WalkDir(m.cfg.EnabledDir(), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if core.NormalizeName(d.Name()) == name {
			_ = os.Remove(path)
		}
		return nil
	})
	links := m.readEnabledLinks()

	m.mu.Lock()
	defer m.mu.Unlock()
	rt, err = m.lookup(name)
	if err != nil {
		return nil, err
	}
	if err := m.rebuildGraphWithLinksLocked(links); err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	rt.enabled = false
	rt.targets = nil
	return &protocol.EnableResult{Unit: name, Enabled: false}, nil
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
func (m *Manager) readEnabledLinks() map[string][]string {
	dir := m.cfg.EnabledDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make(map[string][]string)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		target := core.NormalizeName(e.Name())
		files, err := os.ReadDir(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := core.NormalizeName(f.Name())
			if _, kerr := unit.KindFromName(name); kerr != nil {
				continue
			}
			out[target] = append(out[target], name)
		}
	}
	for target := range out {
		sort.Strings(out[target])
	}
	return out
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

func (m *Manager) rebuildGraphWithLinksLocked(links map[string][]string) error {
	parsed := m.parsedUnitsLocked()
	g, err := core.Build(withEnabledWants(parsed, links))
	if err != nil {
		return err
	}
	m.graph = g
	for name, rt := range m.units {
		targets := enabledTargetsFrom(links, name)
		rt.enabled = len(targets) > 0
		rt.targets = targets
	}
	return nil
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
