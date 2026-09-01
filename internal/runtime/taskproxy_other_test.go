//go:build !windows

package runtime

import "testing"

func TestStubTaskScheduler(t *testing.T) {
	t.Parallel()
	ts := DefaultTaskScheduler()
	if _, err := ts.Query(`\Backups\LegacyBackup`); err == nil {
		t.Fatal("Linux stub Query must fail")
	}
	if _, err := ts.Start(nil, `\Backups\LegacyBackup`, 0); err == nil {
		t.Fatal("Linux stub Start must fail")
	}
	if _, err := ts.Stop(nil, `\Backups\LegacyBackup`, 0); err == nil {
		t.Fatal("Linux stub Stop must fail")
	}
}
