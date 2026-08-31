package journal

import (
	"strings"
	"testing"
)

func TestNewInvocationIDFormatAndUnique(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		id := NewInvocationID()
		if !ValidInvocationID(id) {
			t.Fatalf("id %q is not a UUID", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestValidInvocationID(t *testing.T) {
	t.Parallel()
	if !ValidInvocationID("94d93c37-6f04-4e3f-8d5d-a1c01dfe67cf") {
		t.Fatal("example from DESIGN.md §24")
	}
	if !ValidInvocationID(NewInvocationID()) {
		t.Fatal("generated")
	}
	if ValidInvocationID("") || ValidInvocationID("not-a-uuid") {
		t.Fatal("invalid accepted")
	}
	if ValidInvocationID(strings.Repeat("a", 36)) {
		t.Fatal("missing hyphens")
	}
}

func TestInjectEnv(t *testing.T) {
	t.Parallel()
	got := InjectEnv([]string{"FOO=bar"}, "abc")
	if len(got) != 2 || got[1] != EnvInvocationID+"=abc" {
		t.Fatalf("inject = %v", got)
	}
	got = InjectEnv([]string{EnvInvocationID + "=old", "FOO=bar"}, "new")
	if got[0] != EnvInvocationID+"=new" {
		t.Fatalf("replace = %v", got)
	}
	if InjectEnv([]string{"FOO=bar"}, "")[0] != "FOO=bar" {
		t.Fatal("empty id must not add")
	}
}
