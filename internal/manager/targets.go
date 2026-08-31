package manager

import "github.com/PLN/winunitd/internal/unit"

const (
	// DefaultTarget is the boot target (DESIGN.md §11, §12).
	DefaultTarget = "default.target"
	// TimersTarget groups enabled timer units. Boot of default.target
	// Wants= this target so enabled timers arm (M9).
	TimersTarget = "timers.target"
	// ShutdownTarget exists for ordered stop (M10).
	ShutdownTarget = "shutdown.target"
)

// builtinTargets are always loaded unless a unit file of the same name
// is present on disk. network-online.target is not shipped.
var builtinTargets = []builtinTarget{
	{DefaultTarget, "Default boot target"},
	{TimersTarget, "Timer units"},
	{ShutdownTarget, "Shutdown"},
}

type builtinTarget struct {
	name        string
	description string
}

func builtinUnit(bt builtinTarget) *unit.Unit {
	u := &unit.Unit{
		Name:        bt.name,
		Kind:        unit.KindTarget,
		Description: bt.description,
		Path:        "builtin:" + bt.name,
	}
	if bt.name == DefaultTarget {
		// Boot starts default.target; pull timers.target so enabled timers
		// arm after boot (DESIGN.md §17). Disk default.target overrides this.
		u.Wants = []string{TimersTarget}
		u.After = []string{TimersTarget}
	}
	return u
}

func mergeBuiltins(loaded []*unit.Unit) []*unit.Unit {
	seen := make(map[string]struct{}, len(loaded)+len(builtinTargets))
	for _, u := range loaded {
		if u != nil {
			seen[u.Name] = struct{}{}
		}
	}
	out := append([]*unit.Unit(nil), loaded...)
	for _, bt := range builtinTargets {
		if _, ok := seen[bt.name]; ok {
			continue
		}
		out = append(out, builtinUnit(bt))
	}
	return out
}
