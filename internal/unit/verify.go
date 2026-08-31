package unit

import (
	"os"
)

// VerifyPath reads a unit file from disk, parses it, and verifies it.
// It does not require a running daemon.
func VerifyPath(path string) Report {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{Issues: []Issue{{
			Path:     path,
			Severity: SeverityError,
			Message:  err.Error(),
		}}}
	}
	name := UnitNameFromPath(path)
	return Parse(path, name, data)
}
