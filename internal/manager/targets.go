package manager

import "github.com/PLN/winunitd/internal/unit"

const (
	// DefaultTarget is the boot target (DESIGN.md §11, §12).
	DefaultTarget = "default.target"
	// TimersTarget groups enabled timer units. Boot of default.target
	// Wants= this target so enabled timers arm (M9).
	TimersTarget = "timers.target"
	// ShutdownTarget is the manager-stop transaction root (DESIGN.md §42).
	ShutdownTarget = "shutdown.target"
	// GraphicalSessionTarget is Active while this SID has a suitable
	// interactive session. User-scope only (DESIGN.md §11, §16). SessionPolicy
	// is not implemented (phase 3 / §76).
	GraphicalSessionTarget = "graphical-session.target"
)

// builtinTargets are always loaded unless a unit file of the same name
// is present on disk. network-online.target is not shipped.
var builtinTargets = []builtinTarget{
	{DefaultTarget, "Default boot target"},
	{TimersTarget, "Timer units"},
	{ShutdownTarget, "Shutdown"},
}

// userBuiltinTargets are loaded only in a per-user manager.
var userBuiltinTargets = []builtinTarget{
	{GraphicalSessionTarget, "Graphical session"},
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

func mergeBuiltins(loaded []*unit.Unit, userScope bool) []*unit.Unit {
	targets := builtinTargets
	if userScope {
		all := make([]builtinTarget, 0, len(builtinTargets)+len(userBuiltinTargets))
		all = append(all, builtinTargets...)
		all = append(all, userBuiltinTargets...)
		targets = all
	}
	seen := make(map[string]struct{}, len(loaded)+len(targets))
	for _, u := range loaded {
		if u != nil {
			seen[u.Name] = struct{}{}
		}
	}
	out := append([]*unit.Unit(nil), loaded...)
	for _, bt := range targets {
		if _, ok := seen[bt.name]; ok {
			continue
		}
		out = append(out, builtinUnit(bt))
	}
	return out
}
