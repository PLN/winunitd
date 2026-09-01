// Package core contains the dependency graph and unit state machine.
// Unit file parsing lives in package unit.
//
// Requires= and Wants= pull units into a start or stop transaction. After=
// and Before= only order units already in that transaction: After without
// Requires does not start the dependency. Stop reverses After=/Before=
// (DESIGN.md §42). See DESIGN.md §10, §35, §69, §70.
// Lifecycle states and Restart= decisions live here (DESIGN.md §20, §68).
// core.Step is a closed allowlist: illegal (from, fromSub, event) triples
// return ErrIllegalTransition and do not invent a destination state.
// Requires= start-failure propagates only to not-yet-started jobs
// (queued or Activating); already-Active requirers stay Active.
package core
