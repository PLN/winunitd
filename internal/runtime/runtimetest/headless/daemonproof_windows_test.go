//go:build windows

package headless

import (
	"context"
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
			t.Fatal(err)
		}
		l.Record(journal.DaemonEvent{Code: journal.DaemonEventOpen})
		if err := l.CloseContext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	open()
	first := daemonLogFacts(root)
	if len(first.Errors) != 0 || first.Archive != nil {
		t.Fatalf("first open: %d errors, archive %t", len(first.Errors), first.Archive != nil)
	}
	p := &DaemonLogProof{SID: self, Root: root, After: first}
	for _, problem := range CheckDaemonLog(p, CheckProtection, self, "", ModeSystem, nil) {
		t.Errorf("the product's own log: %s", problem)
	}
	if _, err := PadLog(DaemonLogPath(root), RotationBytes+1, RotationBytes+4096); err != nil {
		t.Fatal(err)
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
