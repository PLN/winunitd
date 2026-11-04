//go:build !windows

package journal

import "os"

// Non-Windows builds keep the original mode; the Windows implementation owns
// the protected descriptor for journals and archives.
func openJournalFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
}

func protectJournalArchive(string) error { return nil }
