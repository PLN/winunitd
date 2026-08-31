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
package protocol
