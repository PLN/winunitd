package journal

// FormatVersion is the on-disk journal record version (DESIGN.md §22, §53).
// Writers emit v=4: raw stream capture leaves severity unknown. V3 introduced
// continuation/partial fragments; v2 added severity/session/user SID. Readers
// preserve recorded values from mixed v1-v4 files without rewriting old lines.
const FormatVersion = 4

// Explicit severity values. Raw stdout/stderr capture has unknown severity;
// stream identity or message text does not establish an application log level.
const (
	SeverityInfo = "info"
	SeverityErr  = "err"
)

// Origin is user-manager / unit runtime identity copied onto each v=2
// line when known. System-scope and linger-without-session leave fields
// empty.
type Origin struct {
	Session string // Windows session ID as a decimal string
	UserSID string // NT SID of the user manager
}
