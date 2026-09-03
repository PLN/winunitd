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
// DefaultAuthorizer (system pipe) and UserAuthorizer (user pipe) derive
// Peer from the named-pipe client token (ImpersonateNamedPipeClient
// after an identification-level dial; GetNamedPipeClientProcessId only
// if impersonation fails; CheckTokenMembership for Administrators). The
// DACL is defense-in-depth. A user-pipe client is the connecting user
// (Owner), not Administrator unless the token is. Test authorizers
// (AllowAdmin, AllowOwner) are for unit tests only.
//
// ListenPipe / ListenPipeSDDL create every winunitd named pipe (system
// control, user control, notify) with PIPE_REJECT_REMOTE_CLIENTS and
// first-instance-only (FILE_FLAG_FIRST_PIPE_INSTANCE / NT FILE_CREATE).
// A name that is already taken fails closed.
//
// DialDefault verifies the server process is LocalSystem or
// Administrators (token owner SID, token user LocalSystem, or
// CheckTokenMembership). DialUser verifies the server token user SID is
// that user-manager identity. Mismatch is a squat: the connection is
// closed and no RPC is sent. DialPipe does not check (notify / tests).
// DialPipe / DialDefault / DialUser use PipeDialImpLevel (identification),
// not SECURITY_ANONYMOUS.
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
