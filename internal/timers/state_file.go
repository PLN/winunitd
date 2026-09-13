package timers

import (
	"errors"
	"fmt"
	"io"
	"os"
)

type stateFile interface {
	io.WriteCloser
	Sync() error
	Name() string
}

// A store's immutable adapter is also the fault boundary for native file tests.
// No global hook or decision lock is involved in a persistence operation.
type stateFileOps struct {
	create  func(string) (stateFile, error)
	replace func(string, string) error
}

func defaultStateFileOps() stateFileOps {
	return stateFileOps{
		create:  func(dir string) (stateFile, error) { return os.CreateTemp(dir, ".timer-*") },
		replace: replaceStateFile,
	}
}

func writeStateFile(dir, path string, data []byte, override *stateFileOps) error {
	ops := defaultStateFileOps()
	if override != nil {
		ops = *override
	}
	f, err := ops.create(dir)
	if err != nil {
		return fmt.Errorf("create timer state: %w", err)
	}
	defer os.Remove(f.Name())
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("flush timer state: %w", err)
	}
	if err := ops.replace(f.Name(), path); err != nil {
		// Replacement may have completed before the durability acknowledgement failed.
		// The caller must suspend dispatch even if a complete new record is readable.
		return fmt.Errorf("replace timer state: %w", err)
	}
	return nil
}
