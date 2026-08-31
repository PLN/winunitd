package core

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PLN/winunitd/internal/unit"
)

// Graph is the static dependency graph of loaded units.
type Graph struct {
	nodes map[string]*node
}

type node struct {
	name     string
	unit     *unit.Unit
	requires []string
	wants    []string
	after    []string // declared After=
	before   []string // declared Before=
	waitsFor []string // ordering: this unit starts after these loaded units
}

// Build constructs a graph from parsed units. It does not start anything.
// Duplicate names (after normalization) are an error. Ordering cycles are
// not an error here; use OrderingCycle and PlanStart to report them.
func Build(units []*unit.Unit) (*Graph, error) {
	g := &Graph{nodes: make(map[string]*node, len(units))}
	for i, u := range units {
		if u == nil {
			return nil, fmt.Errorf("unit %d is nil", i)
		}
		name := NormalizeName(u.Name)
		if name == "" {
			return nil, fmt.Errorf("unit %d has an empty name", i)
		}
		if _, exists := g.nodes[name]; exists {
			return nil, fmt.Errorf("duplicate unit %q", name)
		}
		g.nodes[name] = &node{
			name:     name,
			unit:     u,
			requires: uniqueStable(u.Requires),
			wants:    uniqueStable(u.Wants),
			after:    uniqueStable(u.After),
			before:   uniqueStable(u.Before),
		}
	}
	for _, n := range g.nodes {
		var waits []string
		for _, dep := range n.after {
			if _, ok := g.nodes[dep]; ok {
				waits = append(waits, dep)
			}
		}
		n.waitsFor = uniqueStable(waits)
	}
	for _, n := range g.nodes {
		for _, later := range n.before {
			o, ok := g.nodes[later]
			if !ok {
				continue
			}
			if !containsName(o.waitsFor, n.name) {
				o.waitsFor = append(o.waitsFor, n.name)
			}
		}
	}
	return g, nil
}

// Names returns loaded unit names in sorted order.
func (g *Graph) Names() []string {
	if g == nil {
		return nil
	}
	names := make([]string, 0, len(g.nodes))
	for name := range g.nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Unit returns the parsed unit for name, or nil if it is not loaded.
func (g *Graph) Unit(name string) *unit.Unit {
	n := g.node(name)
	if n == nil {
		return nil
	}
	return n.unit
}

func (g *Graph) node(name string) *node {
	if g == nil {
		return nil
	}
	return g.nodes[NormalizeName(name)]
}

// Requires returns the required unit names declared by name.
func (g *Graph) Requires(name string) []string {
	n := g.node(name)
	if n == nil {
		return nil
	}
	return cloneNames(n.requires)
}

// Wants returns the wanted unit names declared by name.
func (g *Graph) Wants(name string) []string {
	n := g.node(name)
	if n == nil {
		return nil
	}
	return cloneNames(n.wants)
}

// After returns the ordering predecessors declared by name (After=).
func (g *Graph) After(name string) []string {
	n := g.node(name)
	if n == nil {
		return nil
	}
	return cloneNames(n.after)
}

// WaitsFor returns loaded units that must start before name when both
// are in a transaction (After= plus reverse Before=).
func (g *Graph) WaitsFor(name string) []string {
	n := g.node(name)
	if n == nil {
		return nil
	}
	return cloneNames(n.waitsFor)
}

// OrderingCycle reports an After/Before cycle in the loaded graph.
// The error path is the full closed walk, not merely "cycle detected".
func (g *Graph) OrderingCycle() *CycleError {
	if g == nil {
		return nil
	}
	names := g.Names()
	waits := make(map[string][]string, len(g.nodes))
	for _, name := range names {
		waits[name] = g.nodes[name].waitsFor
	}
	return findCycle(names, waits)
}

// CycleError is an ordering cycle with an explicit path (DESIGN.md §35).
type CycleError struct {
	// Path is a closed walk: A, B, C, A meaning A After B After C After A.
	Path []string
}

func (e *CycleError) Error() string {
	if e == nil || len(e.Path) < 2 {
		return "ordering cycle"
	}
	var b strings.Builder
	for i, name := range e.Path {
		if i > 0 {
			b.WriteString(" After ")
		}
		b.WriteString(name)
	}
	return b.String()
}

// Detail formats the cycle as a per-unit After= explanation.
func (e *CycleError) Detail() string {
	if e == nil || len(e.Path) < 2 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(e.Path)-1; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s\n  After=%s\n", e.Path[i], e.Path[i+1])
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func findCycle(names []string, waitsFor map[string][]string) *CycleError {
	if len(names) == 0 {
		return nil
	}
	sorted := cloneNames(names)
	sort.Strings(sorted)

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(sorted))
	parent := make(map[string]string, len(sorted))
	inGraph := make(map[string]struct{}, len(sorted))
	for _, n := range sorted {
		inGraph[n] = struct{}{}
	}

	var dfs func(u string) *CycleError
	dfs = func(u string) *CycleError {
		color[u] = gray
		succ := cloneNames(waitsFor[u])
		sort.Strings(succ)
		for _, v := range succ {
			if _, ok := inGraph[v]; !ok {
				continue
			}
			switch color[v] {
			case gray:
				path := reconstructCycle(parent, u, v)
				return &CycleError{Path: path}
			case white:
				parent[v] = u
				if c := dfs(v); c != nil {
					return c
				}
			}
		}
		color[u] = black
		return nil
	}

	for _, n := range sorted {
		if color[n] == white {
			if c := dfs(n); c != nil {
				return c
			}
		}
	}
	return nil
}

func reconstructCycle(parent map[string]string, u, v string) []string {
	// Edge u -> v with v gray: v is an ancestor of u in the After walk.
	if u == v {
		return []string{v, v}
	}
	var stack []string
	for x := u; x != v; x = parent[x] {
		if x == "" {
			return []string{v, u, v}
		}
		stack = append(stack, x)
		if len(stack) > len(parent)+1 {
			return []string{v, u, v}
		}
	}
	path := make([]string, 0, len(stack)+2)
	path = append(path, v)
	for i := len(stack) - 1; i >= 0; i-- {
		path = append(path, stack[i])
	}
	path = append(path, v)
	return path
}
