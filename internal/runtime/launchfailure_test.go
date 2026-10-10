package runtime_test

import (
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// launchFailure is a launch error without the executable's directory,
// which can be private: the operation, the file name and the cause.
func launchFailure(err error) string {
	var pe *fs.PathError
	var ee *exec.Error
	switch {
	case errors.As(err, &pe):
		return pe.Op + " " + filepath.Base(pe.Path) + ": " + pe.Err.Error()
	case errors.As(err, &ee):
		return filepath.Base(ee.Name) + ": " + ee.Err.Error()
	}
	return err.Error()
}

// A missing helper executable is reported by its name, never by the
// private directory that holds it.
func TestLaunchFailureNamesNoDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private-profile")
	err := exec.Command(filepath.Join(dir, "runtime.test.exe"), "-winunitd-helper=security-report").Start()
	if err == nil {
		t.Fatal("a missing executable started")
	}
	msg := launchFailure(err)
	if strings.Contains(msg, "private-profile") || !strings.Contains(msg, "runtime.test.exe") {
		t.Fatalf("diagnostic %q", msg)
	}
	if msg := launchFailure(&exec.Error{Name: filepath.Join(dir, "x.exe"), Err: exec.ErrNotFound}); strings.Contains(msg, "private-profile") {
		t.Fatalf("lookup diagnostic %q", msg)
	}
}
