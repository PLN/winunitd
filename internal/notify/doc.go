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
// The parser and fake TCP listener are GOOS-independent so protocol tests
// pass on Linux. Production listen/dial use a Windows named pipe.
package notify
