package runtime

import "testing"

func TestSIDHasInteractiveSessionInvalidSID(t *testing.T) {
	t.Parallel()
	if SIDHasInteractiveSession("") {
		t.Fatal("empty SID")
	}
	if SIDHasInteractiveSession("not-a-sid") {
		t.Fatal("invalid SID must not have an interactive session")
	}
}
