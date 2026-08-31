package unit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.service")
	if err := os.WriteFile(ok, []byte(`
[Service]
Type=oneshot
ExecStart=C:\Tools\run-once.exe
WorkingDirectory=C:\Tools
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := VerifyPath(ok)
	if rep.HasError() {
		t.Fatalf("unexpected errors: %v", issueTexts(rep.Errors()))
	}
	if rep.Unit == nil || rep.Unit.Name != "ok.service" {
		t.Fatalf("unit name = %+v", rep.Unit)
	}

	missing := filepath.Join(dir, "nope.service")
	rep = VerifyPath(missing)
	if !rep.HasError() {
		t.Fatal("missing file should fail")
	}

	bad := filepath.Join(dir, "bad.service")
	if err := os.WriteFile(bad, []byte(`
[Service]
ExecStart=relative.exe
Conflicts=other.service
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep = VerifyPath(bad)
	if !rep.HasError() {
		t.Fatal("expected errors")
	}
	errs := strings.Join(issueTexts(rep.Errors()), "\n")
	if !strings.Contains(errs, "absolute path") {
		t.Fatalf("missing relative ExecStart error: %s", errs)
	}
	if !strings.Contains(errs, `unknown directive "Conflicts"`) {
		t.Fatalf("unknown directive should fail verify: %s", errs)
	}
	if !strings.Contains(errs, "WorkingDirectory is omitted") {
		// WorkingDirectory is a warning, not in Errors
	}
	warns := strings.Join(issueTexts(rep.Warnings()), "\n")
	if !strings.Contains(warns, "WorkingDirectory is omitted") {
		t.Fatalf("missing WorkingDirectory warning: %s", warns)
	}
}

func TestVerifyTimerFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "nightly.timer")
	if err := os.WriteFile(p, []byte(`
[Timer]
OnCalendar=Mon..Fri 03:00
Persistent=true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := VerifyPath(p)
	if rep.HasError() {
		t.Fatalf("errors: %v", issueTexts(rep.Errors()))
	}
	if rep.Unit.Timer.Unit != "nightly.service" {
		t.Fatalf("implicit unit = %q", rep.Unit.Timer.Unit)
	}
}
