package headless

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// PadLog is H12's declared intervention on a stopped daemon log: append
// only spaces and one newline until the file holds at least minBytes, never
// reading or growing past maxBytes, and leave its security unchanged. It is
// not daemon output and carries no sequence or event identity.
func PadLog(path string, minBytes, maxBytes int64) (int64, error) {
	if minBytes < 1 || maxBytes < minBytes {
		return 0, errors.New("invalid padding bounds")
	}
	before, err := securityFingerprint(path)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return 0, err
	}
	size := fi.Size()
	if !fi.Mode().IsRegular() || size > maxBytes {
		_ = f.Close()
		return size, fmt.Errorf("log is not a regular file of at most %d bytes", maxBytes)
	}
	if need := minBytes - size; need > 0 {
		pad := strings.Repeat(" ", int(need-1)) + "\n"
		if _, err := f.WriteString(pad); err != nil {
			_ = f.Close()
			return size, err
		}
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return size, err
	}
	fi, err = os.Stat(path)
	if err != nil {
		return 0, err
	}
	after, err := securityFingerprint(path)
	if err != nil {
		return fi.Size(), err
	}
	if after != before {
		return fi.Size(), errors.New("padding changed the log's security")
	}
	return fi.Size(), nil
}

func runPadLog(args []string) error {
	fs := newFlags("pad-log")
	path := fs.String("path", "", "")
	minBytes := fs.Int64("min-bytes", 262145, "")
	maxBytes := fs.Int64("max-bytes", 300000, "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := absPath("path", *path); err != nil {
		return err
	}
	_, err := PadLog(*path, *minBytes, *maxBytes)
	return err
}
