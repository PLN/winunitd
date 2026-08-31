// Package manager is the in-process unit manager used by winunitd.
//
// It loads unit files, tracks enablement and lifecycle state, and serves
// the control protocol. Start/stop are stubs that update state through the
// dependency graph; they do not create Job Objects or processes (M3).
package manager
