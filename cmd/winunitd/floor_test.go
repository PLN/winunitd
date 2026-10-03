package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/servicing"
)

func floorBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	return base
}

func runFloorArgs(build servicing.Build, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := runFloor(args, &out, &errb, build)
	return code, out.String(), errb.String()
}

func TestFloorVerbs(t *testing.T) {
	base := floorBase(t)
	clean := false
	build := servicing.Build{Version: "0.2.0", Commit: "abc", Modified: &clean, Features: []string{"exec-stop"}}
	if code, out, _ := runFloorArgs(build, "show", "--base-dir", base); code != 0 || !strings.Contains(out, "no compatibility floor") {
		t.Fatalf("show without floor: %d %q", code, out)
	}
	if code, out, _ := runFloorArgs(build, "check", "--base-dir", base); code != 0 || !strings.Contains(out, "admission allowed") {
		t.Fatalf("check without floor: %d %q", code, out)
	}
	code, out, errOut := runFloorArgs(build, "set", "--base-dir", base, "--min-version", "0.2.0", "--require", "exec-stop", "--require-clean")
	if code != 0 || !strings.Contains(out, "next manager start") {
		t.Fatalf("set: %d %q %q", code, out, errOut)
	}
	if code, out, _ := runFloorArgs(build, "show", "--base-dir", base); code != 0 || !strings.Contains(out, `"minVersion": "0.2.0"`) || !strings.Contains(out, `"exec-stop"`) {
		t.Fatalf("show: %d %q", code, out)
	}
	older := servicing.Build{Version: "0.1.0-alpha"}
	if code, out, _ := runFloorArgs(older, "check", "--base-dir", base); code != 1 || !strings.Contains(out, "below compatibility floor: version 0.1.0-alpha is below 0.2.0") {
		t.Fatalf("check below floor: %d %q", code, out)
	}
	// A floor that the running build would not satisfy is refused and the
	// existing floor is left in place.
	code, _, errOut = runFloorArgs(build, "set", "--base-dir", base, "--min-version", "0.3.0", "--require", "linger-s4u")
	if code != 1 || !strings.Contains(errOut, "version 0.2.0 is below 0.3.0; missing features linger-s4u") {
		t.Fatalf("unsatisfied set: %d %q", code, errOut)
	}
	if f, err := servicing.ReadFloor(servicing.FloorPath(base)); err != nil || f.MinVersion != "0.2.0" {
		t.Fatalf("refused set changed the floor: %+v %v", f, err)
	}
	if code, out, _ := runFloorArgs(build, "clear", "--base-dir", base); code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("clear: %d %q", code, out)
	}
	if code, _, _ := runFloorArgs(older, "check", "--base-dir", base); code != 0 {
		t.Fatal("cleared floor still holds")
	}
}

func TestFloorUsageAndUntrustedRecord(t *testing.T) {
	base := floorBase(t)
	build := servicing.Build{Version: "0.2.0"}
	for _, args := range [][]string{
		nil, {"raise"}, {"show", "extra"}, {"set", "--base-dir", base}, {"set", "--base-dir", base, "--min-version", "two"},
		{"check", "--min-version", "0.2.0"}, {"set", "--base-dir", base, "--require", "Exec Stop"},
	} {
		if code, _, _ := runFloorArgs(build, args...); code != 2 {
			t.Errorf("%q exit %d, want 2", args, code)
		}
	}
	if err := os.WriteFile(servicing.FloorPath(base), []byte(`{"schema":1,"minVersion":"0.1.0"}`), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(servicing.FloorPath(base), 0o666); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runFloorArgs(build, "check", "--base-dir", base); code != 1 || !strings.Contains(out, "unusable") {
		t.Fatalf("untrusted record: %d %q", code, out)
	}
	if code, _, _ := runFloorArgs(build, "show", "--base-dir", base); code != 1 {
		t.Fatal("show trusted a writable record")
	}
}

func TestRunRoutesFloorVerbs(t *testing.T) {
	base := floorBase(t)
	var out, errb bytes.Buffer
	if code := run([]string{"floor", "check", "--base-dir", base}, &out, &errb); code != 0 || !strings.Contains(out.String(), "admission allowed") {
		t.Fatalf("run floor check: %d %q %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"floor", "--help"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "compat-floor.json") {
		t.Fatalf("floor help exit %d: %q", code, out.String())
	}
}

func TestStartupHoldFollowsTheFloor(t *testing.T) {
	base := floorBase(t)
	build := servicing.Build{Version: "0.1.0-alpha"}
	if hold := startupHold(base, build); hold != "" {
		t.Fatalf("no floor: %q", hold)
	}
	if err := servicing.WriteFloor(servicing.FloorPath(base), &servicing.Floor{Schema: 1, MinVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if hold := startupHold(base, build); !strings.HasPrefix(hold, "below compatibility floor: ") {
		t.Fatalf("hold = %q", hold)
	}
}
