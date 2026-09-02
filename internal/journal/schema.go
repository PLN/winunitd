package journal

// FormatVersion is the on-disk journal record version (DESIGN.md §22, §53).
// Writers emit v=2. Readers accept mixed files: v=1 lines decode with
// empty severity / session / user SID; v=2 carries those fields. Old
// files are not rewritten.
const FormatVersion = 2

// Severity values stored on v=2 lines. Mapped from stream at write
// (stdout → info, stderr → err). There is no unit-file severity API.
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

// SeverityFromStream maps a capture stream to a stored severity.
// Unknown or empty streams yield an empty severity.
func SeverityFromStream(stream string) string {
	switch stream {
	case "stderr":
		return SeverityErr
	case "stdout":
		return SeverityInfo
	default:
		return ""
	}
}
