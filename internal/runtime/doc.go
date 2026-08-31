// Package runtime contains the Windows Service host, the daemon-level Job
// Object used for strict ownership, per-unit Job Objects, CreateProcess
// supervision, WTS tokens, S4U linger tokens, and user-manager process launch.
//
// SCM stop and preshutdown cancel the host; the daemon then stops units in
// reverse After=/Before= order and closes the daemon Job Object. Session
// change notifications start per-user managers (same binary,
// --user-manager <SID>) using WTSQueryUserToken. Lingering user managers
// start at boot with no session via S4U. Each user manager watches WTS
// for its own SID and drives graphical-session.target from
// SIDHasInteractiveSession.
package runtime
