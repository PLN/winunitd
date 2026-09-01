package core

import (
	"fmt"
	"sort"
)

// JobKind is a planned action in a transaction (DESIGN.md §70).
type JobKind int

const (
	JobStart JobKind = iota
	JobStop
)

func (k JobKind) String() string {
	switch k {
	case JobStart:
		return "start"
	case JobStop:
		return "stop"
	default:
		return fmt.Sprintf("job(%d)", int(k))
	}
}

// Job is one unit activation or deactivation in a transaction.
type Job struct {
	Name string
	Kind JobKind
}

// Transaction is a validated start plan. Construct it with PlanStart and
// inspect it before Execute so a bad plan cannot be partially applied.
type Transaction struct {
	g        *Graph
	roots    []string
	jobs     map[string]*Job
	waitsFor map[string][]string // ordering among jobs only
}

// MissingUnitError is returned when a required unit is not loaded.
type MissingUnitError struct {
	Unit       string
	RequiredBy string
}

func (e *MissingUnitError) Error() string {
	if e == nil {
		return "missing unit"
	}
	if e.RequiredBy == "" {
		return fmt.Sprintf("unit %q is not loaded", e.Unit)
	}
	return fmt.Sprintf("unit %q required by %q is not loaded", e.Unit, e.RequiredBy)
}

// PlanStart builds a start transaction for the named units.
// Requires= and BindsTo= pull required dependencies in; Wants= pulls
// optional ones. After=/Before=/PartOf= do not pull. The plan is fully
// validated (missing Requires/BindsTo, ordering cycles) before it is
// returned. No Starter is invoked.
func (g *Graph) PlanStart(names ...string) (*Transaction, error) {
	return g.plan(JobStart, names...)
}

// PlanStop builds a single-unit stop transaction (DESIGN.md §10, §42).
// The job set is the named units plus their reverse requirement closure
// (units that Requires=/BindsTo=/PartOf= a root). Forward Requires=/Wants=
// and pure After=/Before= neighbors are not pulled. Dependents stop
// before the units they require; After=/Before= among jobs are reversed.
func (g *Graph) PlanStop(names ...string) (*Transaction, error) {
	if g == nil {
		return nil, fmt.Errorf("nil graph")
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no unit to stop")
	}

	tx := &Transaction{
		g:        g,
		jobs:     make(map[string]*Job),
		waitsFor: make(map[string][]string),
	}

	roots := make([]string, 0, len(names))
	seenRoot := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = NormalizeName(name)
		if name == "" {
			return nil, fmt.Errorf("empty unit name")
		}
		if _, ok := seenRoot[name]; ok {
			continue
		}
		seenRoot[name] = struct{}{}
		roots = append(roots, name)
		if g.nodes[name] == nil {
			return nil, &MissingUnitError{Unit: name}
		}
	}
	tx.roots = roots

	for _, name := range g.reverseRequirementClosure(roots) {
		tx.jobs[name] = &Job{Name: name, Kind: JobStop}
	}

	tx.waitsFor = mergeWaits(reverseWaits(tx.jobs, g), reverseRequirementWaits(tx.jobs, g))
	if err := tx.validate(); err != nil {
		return nil, err
	}
	return tx, nil
}

// PlanShutdown builds the manager-stop transaction (DESIGN.md §42).
// Requires=/BindsTo=/Wants= pull the same set as start, and units that
// After= a root (transitively) are also pulled — stop everything After=
// the root, reverse of boot. After=/Before= among jobs are reversed.
// Missing Requires= is skipped (already gone), not a plan error.
func (g *Graph) PlanShutdown(names ...string) (*Transaction, error) {
	return g.plan(JobStop, names...)
}

func (g *Graph) plan(kind JobKind, names ...string) (*Transaction, error) {
	if g == nil {
		return nil, fmt.Errorf("nil graph")
	}
	if len(names) == 0 {
		if kind == JobStop {
			return nil, fmt.Errorf("no unit to stop")
		}
		return nil, fmt.Errorf("no unit to start")
	}

	tx := &Transaction{
		g:        g,
		jobs:     make(map[string]*Job),
		waitsFor: make(map[string][]string),
	}

	var add func(name, requiredBy string, required bool) error
	add = func(name, requiredBy string, required bool) error {
		name = NormalizeName(name)
		if name == "" {
			if required && kind == JobStart {
				return &MissingUnitError{Unit: name, RequiredBy: requiredBy}
			}
			return nil
		}
		if _, exists := tx.jobs[name]; exists {
			return nil
		}
		n := g.nodes[name]
		if n == nil {
			if required && (kind == JobStart || requiredBy == "") {
				return &MissingUnitError{Unit: name, RequiredBy: requiredBy}
			}
			return nil
		}
		tx.jobs[name] = &Job{Name: name, Kind: kind}
		for _, dep := range n.requires {
			if err := add(dep, name, true); err != nil {
				return err
			}
		}
		for _, dep := range n.bindsTo {
			if err := add(dep, name, true); err != nil {
				return err
			}
		}
		for _, dep := range n.wants {
			if err := add(dep, name, false); err != nil {
				return err
			}
		}
		return nil
	}

	roots := make([]string, 0, len(names))
	seenRoot := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = NormalizeName(name)
		if name == "" {
			return nil, fmt.Errorf("empty unit name")
		}
		if _, ok := seenRoot[name]; ok {
			continue
		}
		seenRoot[name] = struct{}{}
		roots = append(roots, name)
		if err := add(name, "", true); err != nil {
			return nil, err
		}
	}
	tx.roots = roots

	if kind == JobStop {
		// Shutdown: stop everything After= a root (reverse of boot), without
		// pulling After= of Wants=/Requires= members that were not roots.
		for _, later := range g.afterClosure(roots) {
			if err := add(later, "", false); err != nil {
				return nil, err
			}
		}
	}

	if kind == JobStop {
		tx.waitsFor = reverseWaits(tx.jobs, g)
	} else {
		for name := range tx.jobs {
			n := g.nodes[name]
			var waits []string
			for _, dep := range n.waitsFor {
				if _, in := tx.jobs[dep]; in {
					waits = append(waits, dep)
				}
			}
			tx.waitsFor[name] = uniqueStable(waits)
		}
	}

	if err := tx.validate(); err != nil {
		return nil, err
	}
	return tx, nil
}

func (g *Graph) afterSuccessors(name string) []string {
	name = NormalizeName(name)
	var out []string
	for _, other := range g.Names() {
		n := g.nodes[other]
		if n == nil {
			continue
		}
		if containsName(n.waitsFor, name) {
			out = append(out, other)
		}
	}
	return out
}

func (g *Graph) afterClosure(roots []string) []string {
	seen := make(map[string]struct{})
	var walk func(name string)
	walk = func(name string) {
		for _, later := range g.afterSuccessors(name) {
			if _, ok := seen[later]; ok {
				continue
			}
			seen[later] = struct{}{}
			walk(later)
		}
	}
	for _, r := range roots {
		walk(NormalizeName(r))
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func reverseWaits(jobs map[string]*Job, g *Graph) map[string][]string {
	out := make(map[string][]string, len(jobs))
	for name := range jobs {
		n := g.nodes[name]
		if n == nil {
			continue
		}
		for _, pred := range n.waitsFor {
			if _, in := jobs[pred]; !in {
				continue
			}
			// pred started before name, so name must stop before pred.
			out[pred] = append(out[pred], name)
		}
	}
	for name := range out {
		out[name] = uniqueStable(out[name])
	}
	return out
}

func reverseRequirementWaits(jobs map[string]*Job, g *Graph) map[string][]string {
	out := make(map[string][]string, len(jobs))
	for name := range jobs {
		n := g.nodes[name]
		if n == nil {
			continue
		}
		for _, dep := range n.stopDepNames() {
			if _, in := jobs[dep]; !in {
				continue
			}
			// name Requires=/BindsTo=/PartOf= dep, so name must stop first.
			out[dep] = append(out[dep], name)
		}
	}
	for name := range out {
		out[name] = uniqueStable(out[name])
	}
	return out
}

func mergeWaits(a, b map[string][]string) map[string][]string {
	out := make(map[string][]string, len(a)+len(b))
	for name, waits := range a {
		out[name] = append(out[name], waits...)
	}
	for name, waits := range b {
		out[name] = append(out[name], waits...)
	}
	for name := range out {
		out[name] = uniqueStable(out[name])
	}
	return out
}

func (tx *Transaction) validate() error {
	if tx == nil || len(tx.jobs) == 0 {
		return fmt.Errorf("empty transaction")
	}
	if c := findCycle(tx.jobNames(), tx.waitsFor); c != nil {
		return c
	}
	return nil
}

func (tx *Transaction) jobNames() []string {
	names := make([]string, 0, len(tx.jobs))
	for name := range tx.jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Units returns the start jobs in this transaction, sorted by name.
func (tx *Transaction) Units() []string {
	if tx == nil {
		return nil
	}
	return tx.jobNames()
}

// Jobs returns a copy of the planned jobs, sorted by unit name.
func (tx *Transaction) Jobs() []Job {
	if tx == nil {
		return nil
	}
	names := tx.jobNames()
	out := make([]Job, len(names))
	for i, name := range names {
		out[i] = *tx.jobs[name]
	}
	return out
}

// Ready returns units with no ordering predecessor in this transaction.
// Independent branches appear together so they can start concurrently.
func (tx *Transaction) Ready() []string {
	if tx == nil {
		return nil
	}
	var ready []string
	for _, name := range tx.jobNames() {
		if len(tx.waitsFor[name]) == 0 {
			ready = append(ready, name)
		}
	}
	return ready
}

// Contains reports whether name has a start job in this transaction.
func (tx *Transaction) Contains(name string) bool {
	if tx == nil {
		return false
	}
	_, ok := tx.jobs[NormalizeName(name)]
	return ok
}
