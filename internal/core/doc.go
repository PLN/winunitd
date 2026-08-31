// Package core contains the dependency graph and unit state machine.
// Unit file parsing lives in package unit.
//
// Requires= and Wants= pull units into a start transaction. After= and
// Before= only order units already in that transaction: After without
// Requires does not start the dependency. See DESIGN.md §10, §35, §69, §70.
package core
