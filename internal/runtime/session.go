package runtime

// MaxInteractiveSessions bounds retained session snapshots and host mappings.
// Oversized snapshots are errors, never truncated authoritative logoff lists.
const MaxInteractiveSessions = 4096

// SessionChange is a logon or logoff for one interactive session.
// User managers are SID-keyed; the host maps session IDs to SIDs.
type SessionChange struct {
	SessionID uint32
	Logon     bool
}

// SessionEnumerator lists interactive sessions that should have a user
// manager (logged on; not session 0 / listen sockets).
type SessionEnumerator func() ([]uint32, error)
