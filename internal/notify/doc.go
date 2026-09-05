// Package notify is the native readiness/watchdog protocol (DESIGN.md §9,
// §19.1, §79–80).
//
// Units send sd_notify-shaped newline KEY=VALUE messages on a per-unit
// named pipe:
//
//	\\.\pipe\winunitd\notify\<unit-id>
//
// Before writing, clients read the server's "WINUNITD-NOTIFY/1\n" acceptance
// banner. This prevents a short-lived Windows client from disconnecting before
// the pipe connection is accepted. The banner confirms transport acceptance,
// not that the payload was processed or the unit became ready. The current
// helper requires a matching daemon; older daemons do not send this banner.
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
