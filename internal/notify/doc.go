// Package notify is the native readiness/watchdog protocol (DESIGN.md §9,
// §19.1, §79–80).
//
// Units send sd_notify-shaped newline KEY=VALUE messages on a per-unit
// named pipe:
//
//	\\.\pipe\winunitd\notify\<unit-id>
//
// Environment injected into the unit process:
//
//	WINUNIT_NOTIFY_PIPE
//	WINUNIT_WATCHDOG_USEC
//
// WINUNIT_INVOCATION_ID is injected by the manager on each start; the
// notify pipe name stays the unit name.
//
// The parser and fake TCP listener are GOOS-independent so protocol tests
// pass on Linux. Production listen/dial use a Windows named pipe
// (protocol.ListenPipeSDDL: PIPE_REJECT_REMOTE_CLIENTS and
// first-instance-only, same as the control pipes).
package notify
