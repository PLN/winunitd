// Package manager is the in-process unit manager used by winunitd.
//
// It loads unit files, tracks enablement and lifecycle state, and serves
// the control protocol. Start CreateProcess's ExecStart into a per-unit
// Job Object; stop kills that job. Restart= (no / always / on-failure /
// on-watchdog) relaunches the main process into a new unit job. Type=notify
// stays activating until READY=1. WatchdogSec= uses WINUNIT_NOTIFY_PIPE
// (WatchdogMode=notify, the default) or probes WatchdogEndpoint= (tcp connect
// or http GET, localhost only). A missed/failed probe fails the unit the
// same way as a missed notify heartbeat. Type=scm orchestrates an existing
// SCM service by ServiceName= (StartService / StopService / QueryServiceStatusEx)
// and does not CreateProcess. Restart= applies to SCM start failure only.
// Enable writes files under enabled/<target>/<unit>; boot starts default.target, which Wants=
// timers.target so enabled timers arm. stdout/stderr are stored under
// journal/<unit>.log (each start tagged with InvocationID=) and returned
// by logs. The timer scheduler lives in
// internal/timers (not Task Scheduler). Manager shutdown stops units in
// reverse After=/Before= order (shutdown.target as root) then the daemon
// closes the daemon Job Object.
//
// The system manager also hosts per-user managers (same binary,
// --user-manager <SID>) started on first interactive logon or at boot
// when lingering. User units live in the user manager process and are
// not listed by system list-units. Linger records live under linger\<SID>.
// A user manager watches WTS for its SID and starts/stops builtin
// graphical-session.target from SIDHasInteractiveSession (Inactive when
// lingering with no session).
package manager
