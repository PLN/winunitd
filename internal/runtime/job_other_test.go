//go:build !windows

package runtime

import "testing"

func TestDaemonJobStubOpenClose(t *testing.T) {
	j, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AssignSelf(); err != nil {
		t.Fatal(err)
	}
	if err := j.AssignPID(1); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
}
