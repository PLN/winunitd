// Package runtime contains the Windows Service host, the daemon-level Job
// Object used for strict ownership, and (later) process execution, tokens,
// and sessions.
//
// Per-unit Job Objects, KillMode, breakaway handling, and CreateProcess
// supervision are M5. Ordered stop on SCM shutdown is M10.
package runtime
