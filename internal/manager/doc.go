// Package manager is the in-process unit manager used by winunitd.
//
// It loads unit files, tracks enablement and lifecycle state, and serves
// the control protocol. Start CreateProcess's ExecStart into a per-unit
// Job Object; stop of a unit kills that job plus reverse requirers
// (Requires=/BindsTo=/PartOf=). MemoryMax=, ProcessLimit=, and
// PriorityClass= apply to that existing job (whole tree). Hitting
// MemoryMax= or ProcessLimit= fails the unit with reason resource-limit.
// Restart= (no / always / on-failure /
// on-watchdog) relaunches the main process into a new unit job. StartLimitBurst
// starts inside StartLimitIntervalSec fail the unit with reason start-limit
// instead of scheduling another restart (StartLimitBurst=0 is unlimited;
// explicit Start resets). Type=notify
// stays activating until READY=1. WatchdogSec= uses WINUNIT_NOTIFY_PIPE
// (WatchdogMode=notify, the default) or probes WatchdogEndpoint= (tcp connect
// or http GET, localhost only). A missed/failed probe fails the unit the
// same way as a missed notify heartbeat. Type=scm orchestrates an existing
// SCM service by ServiceName= (StartService / StopService / QueryServiceStatusEx)
// and does not CreateProcess. Restart= applies to SCM start failure only.
// Type=scheduled-task orchestrates an existing Task Scheduler task by TaskName=
// (IRegisteredTask.Run / Stop / State) and does not CreateProcess. Restart=
// applies to Run failure only; once running, Task Scheduler owns the instance.
// Enable writes files under enabled/<target>/<unit>; boot starts default.target, which Wants=
// timers.target so enabled timers arm. stdout/stderr are stored under
// journal/<unit>.log (each start tagged with InvocationID=) and returned
// by logs. The timer scheduler lives in
// internal/timers (not Task Scheduler). .registry units watch
// RegistryChanged= keys (RegNotifyChangeKeyValue on Windows; tests inject
// a stub). .eventlog units watch EventLogTrigger= channels (EvtSubscribe
// on Windows; tests inject a stub). .path units watch PathChanged= files
// and directories (ReadDirectoryChangesW on Windows, non-recursive; tests
// inject a stub). A change or matching event starts the
// basename .service if it is not already running. Manager shutdown stops units in
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
