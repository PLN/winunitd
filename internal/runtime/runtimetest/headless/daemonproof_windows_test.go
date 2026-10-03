//go:build windows

package headless

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/journal"
)

// probe-daemon-log's facts match what the product writes: a protected
// directory and log for the account that opened it, an open record, and,
// after the stopped log is padded and the log reopened, the padded file as
// the archive and a fresh current log.
func TestDaemonLogFactsOfTheProductLog(t *testing.T) {
	root := t.TempDir()
	self := selfSID(t)
	open := func() {
		t.Helper()
		l, err := journal.OpenDaemonLog(root, nil)
		if err != nil {
			t.Fatal(baseOnly(err))
		}
		l.Record(journal.DaemonEvent{Code: journal.DaemonEventOpen})
		if err := l.CloseContext(context.Background()); err != nil {
			t.Fatal(baseOnly(err))
		}
	}
	open()
	first := daemonLogFacts(root)
	if len(first.Errors) != 0 || first.Archive != nil {
		t.Fatalf("first open: %d errors, archive %t", len(first.Errors), first.Archive != nil)
	}
	p := &DaemonLogProof{SID: self, Root: root, After: first}
	for _, problem := range CheckDaemonLog(p, CheckProtection, self, "", ModeSystem, nil) {
		// A SYSTEM runner's scratch root is not the system data root.
		if problem != "the daemon-log facts are not the system manager's data root" {
			t.Errorf("the product's own log: %s", problem)
		}
	}
	if _, err := PadLog(DaemonLogPath(root), RotationBytes+1, RotationBytes+4096); err != nil {
		t.Fatal(baseOnly(err))
	}
	before := daemonLogFacts(root)
	open()
	after := daemonLogFacts(root)
	switch {
	case before.Current == nil || before.Current.Size <= RotationBytes:
		t.Fatal("the stopped log was not padded")
	case after.Archive == nil || after.Archive.SHA256 != before.Current.SHA256:
		t.Fatal("the padded log was not rotated to the archive unchanged")
	case after.Current == nil || after.Current.Size >= RotationBytes || !protectedDACL(after.Archive.DACL, self):
		t.Fatal("no fresh protected log after the rotation")
	}
}

// DaemonLogPath is the product's current log under a data root.
func DaemonLogPath(root string) string { return journal.DaemonLogPath(root) }

// unit-status names a missing configured winctl by its file name only; the
// directory, which can be an account's profile, stays out of the error.
func TestUnitStatusMissingWinctlNamesNoDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private-profile")
	config := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(ProbeConfig{Winctl: filepath.Join(dir, "winctl.exe"), StatusUnit: "failing.service"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, data, 0o600); err != nil {
		t.Fatal(baseOnly(err))
	}
	err = runUnitStatus([]string{"--config", config, "--out", filepath.Join(t.TempDir(), "status.json")})
	switch {
	case err == nil:
		t.Fatal("a missing winctl produced a status")
	case strings.Contains(err.Error(), "private-profile"):
		t.Fatal("the diagnostic names the private directory")
	case !strings.Contains(err.Error(), "winctl.exe"):
		t.Fatal("the diagnostic does not name the executable")
	}
}
