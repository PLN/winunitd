// Package runtime contains the Windows Service host, the daemon-level Job
// Object used for strict ownership, per-unit Job Objects, and CreateProcess
// supervision.
//
// SCM stop and preshutdown cancel the host; the daemon then stops units in
// reverse After=/Before= order and closes the daemon Job Object.
package runtime
