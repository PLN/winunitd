// Package protocol is the versioned JSON-RPC control API for winunitd.
//
// Local control uses a protected named pipe (DESIGN.md §30):
//
//	\\.\pipe\winunitd\control
//
// The protocol is the API. CLI output is a presentation of RPC results and
// is not parsed by tests or by the daemon. Messages carry protocol name
// and version so clients and servers can reject mismatches.
//
// Per-user managers listen on \\.\pipe\winunitd\user\<SID>\control
// (that user, LocalSystem, and Administrators). winctl --user dials
// that pipe; bare winctl stays on the system pipe.
// enable-linger / disable-linger are administrator verbs on the system
// pipe only.
//
// Transport is newline-delimited compact JSON over a byte stream, so the
// same codec and handler can run on a Windows named pipe or on a fake
// net.Listener in tests (no Windows service required).
//
// logs (DESIGN.md §22) is a snapshot RPC, not a stream:
//
//	LogsParams:  unit, since, follow, cursor
//	LogsResult:  unit, entries, cursor
//
// LogEntry is the v=2 journal record (timestamp, unit, pid, stream,
// message, invocationId, severity, session, userSid). Historical v=1
// lines decode with empty severity/session/userSid.
//
// since is a lower bound (RFC3339, YYYY-MM-DD, Go duration, or
// "N <unit> ago"). Invalid since is invalid-params — never a silent
// ignore. follow waits briefly for new lines after cursor; winctl
// --follow polls with the opaque cursor from the previous result.
package protocol
