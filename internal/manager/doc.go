// Package manager is the in-process unit manager used by winunitd.
//
// It loads unit files, tracks enablement and lifecycle state, and serves
// the control protocol. Start/stop still update graph state only; they do
// not spawn processes or per-unit Job Objects (M5). Daemon-level ownership
// lives in package runtime.
package manager
