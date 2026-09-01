// Package core contains the dependency graph and unit state machine.
// Unit file parsing lives in package unit.
// Unit names are case-insensitive and stored lower-case (DESIGN.md §36).
//
// Requires= and Wants= pull units into a start transaction; BindsTo=
// pulls like Requires=. After=/Before=/PartOf= do not pull on start.
// After without Requires does not start the dependency.
// Single-unit stop (PlanStop) is the reverse requirement closure
// (Requires=/BindsTo=/PartOf= of the root), not forward deps and not
// After= neighbors. Shutdown (PlanShutdown, root shutdown.target) stops
// every pulled unit in reverse After=/Before= order (DESIGN.md §10, §42).
// See DESIGN.md §35, §69, §70.
// Lifecycle states, Restart=, and StartLimitBurst / StartLimitIntervalSec
// decisions live here (DESIGN.md §20, §44, §68).
// core.Step is a closed allowlist: illegal (from, fromSub, event) triples
// return ErrIllegalTransition and do not invent a destination state.
// Requires= start-failure propagates only to not-yet-started jobs
// (queued or Activating); already-Active requirers stay Active.
package core
