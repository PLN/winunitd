// Package manager is the in-process unit manager used by winunitd.
//
// It loads unit files, tracks enablement and lifecycle state, and serves
// the control protocol. Start CreateProcess's ExecStart into a per-unit
// Job Object; stop kills that job. Restart= (no / always / on-failure)
// relaunches the main process into a new unit job. Enable writes files
// under enabled/<target>/<unit>; boot starts default.target, which Wants=
// timers.target so enabled timers arm. stdout/stderr are stored under
// journal/<unit>.log and returned by logs. The timer scheduler lives in
// internal/timers (not Task Scheduler). Manager shutdown stops units in
// reverse After=/Before= order (shutdown.target as root) then the daemon
// closes the daemon Job Object.
//
// The system manager also hosts per-user managers (same binary,
// --user-manager <SID>) started on first interactive logon. User units
// live in the user manager process and are not listed by system
// list-units.
package manager
