// Package runtime contains the Windows Service host, the daemon-level Job
// Object used for strict ownership, per-unit Job Objects (including R1
// MemoryMax=/ProcessLimit=/PriorityClass= and R2 CPUWeight=/CPUQuota=/
// IoPriority=), CreateProcess
// supervision, WTS tokens, S4U linger tokens, user-manager process launch,
// and the Type=scm SCM proxy client (StartService / StopService /
// QueryServiceStatusEx; never CreateService / ChangeServiceConfig / Delete),
// and the Type=scheduled-task Task Scheduler proxy client (IRegisteredTask.Run /
// Stop / State; never RegisterTask / DeleteTask / put_Enabled).
//
// SCM stop and preshutdown cancel the host; the daemon then stops units in
// reverse After=/Before= order and closes the daemon Job Object. Session
// change notifications start per-user managers (same binary,
// --user-manager <SID>) using WTSQueryUserToken. TIMECHANGE and POWEREVENT
// (PBT_APMRESUMEAUTOMATIC / PBT_APMRESUMESUSPEND) call Engine.ClockChanged
// so calendar timers recompute without waiting for the 30s poll. Console
// and user-manager processes have no hidden WM_TIMECHANGE window; they
// keep the poll. Lingering user managers
// start at boot with no session via trusted-LSA S4U (optional named
// CredMan/LSA URI on the linger record if S4U lacks outbound network
// creds). Each user manager watches WTS
// for its own SID and drives graphical-session.target from
// SIDHasInteractiveSession.
package runtime
