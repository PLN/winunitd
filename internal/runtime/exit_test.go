package runtime

import "testing"

func TestExitStatus(t *testing.T) {
	t.Parallel()
	e := &ExitStatus{Code: 2}
	if e.Error() != "exit status 2" {
		t.Fatalf("error = %q", e.Error())
	}
	if !e.Failed() {
		t.Fatal("nonzero should be failed")
	}
	if (&ExitStatus{Code: 0}).Failed() {
		t.Fatal("zero should not be failed")
	}
	if (*ExitStatus)(nil).Failed() {
		t.Fatal("nil should not be failed")
	}
}
