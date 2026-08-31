package core

import (
	"fmt"
	"sort"
)

// JobKind is a planned action in a transaction (DESIGN.md §70).
type JobKind int

const (
	JobStart JobKind = iota
)

func (k JobKind) String() string {
	switch k {
	case JobStart:
		return "start"
	default:
		return fmt.Sprintf("job(%d)", int(k))
	}
}

// Job is one unit activation in a transaction.
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
// Requires= and Wants= pull dependencies in; After=/Before= do not.
// The plan is fully validated (missing Requires, ordering cycles) before
// it is returned. No Starter is invoked.
func (g *Graph) PlanStart(names ...string) (*Transaction, error) {
	if g == nil {
		return nil, fmt.Errorf("nil graph")
	}
	if len(names) == 0 {
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
			if required {
				return &MissingUnitError{Unit: name, RequiredBy: requiredBy}
			}
			return nil
		}
		if _, exists := tx.jobs[name]; exists {
			return nil
		}
		n := g.nodes[name]
		if n == nil {
			if required {
				return &MissingUnitError{Unit: name, RequiredBy: requiredBy}
			}
			return nil
		}
		tx.jobs[name] = &Job{Name: name, Kind: JobStart}
		for _, dep := range n.requires {
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

	if err := tx.validate(); err != nil {
		return nil, err
	}
	return tx, nil
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
