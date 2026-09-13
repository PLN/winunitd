//go:build !windows

package timers

import (
	"errors"
	"os"
	"path/filepath"
)

func replaceStateFile(temporary, path string) error {
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	// File.Sync does not persist the directory entry that publishes the record.
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
