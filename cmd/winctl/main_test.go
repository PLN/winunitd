package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--help"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "verify") {
		t.Fatalf("help missing verify: %s", out.String())
	}
}

func TestRunVerify(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.service")
	if err := os.WriteFile(ok, []byte(`
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`), 0o644); err != nil {
		t.Fatal(err)
	}
	warn := filepath.Join(dir, "warn.service")
	if err := os.WriteFile(warn, []byte(`
[Service]
ExecStart=C:\Tools\foo.exe
`), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.service")
	if err := os.WriteFile(bad, []byte(`
[Service]
ExecStart=foo.exe
WatchdogSec=1s
`), 0o644); err != nil {
		t.Fatal(err)
	}
	timer := filepath.Join(dir, "foo.timer")
	if err := os.WriteFile(timer, []byte(`
[Timer]
OnCalendar=daily
`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("ok", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"verify", ok}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit %d stdout=%s stderr=%s", code, out.String(), errb.String())
		}
		if !strings.Contains(out.String(), "ok.service: verified") {
			t.Fatalf("stdout=%s", out.String())
		}
	})

	t.Run("working directory warning still verifies", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"verify", warn}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit %d stdout=%s stderr=%s", code, out.String(), errb.String())
		}
		if !strings.Contains(errb.String(), "WorkingDirectory is omitted") {
			t.Fatalf("stderr=%s", errb.String())
		}
		if !strings.Contains(out.String(), "warn.service: verified") {
			t.Fatalf("stdout=%s", out.String())
		}
	})

	t.Run("unknown and relative fail", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"verify", bad}, &out, &errb)
		if code != 1 {
			t.Fatalf("exit %d, want 1; stdout=%s stderr=%s", code, out.String(), errb.String())
		}
		s := errb.String()
		if !strings.Contains(s, "absolute path") {
			t.Fatalf("missing relative ExecStart: %s", s)
		}
		if !strings.Contains(s, `unknown directive "WatchdogSec"`) {
			t.Fatalf("unknown directive should fail: %s", s)
		}
		if strings.Contains(out.String(), ": verified") {
			t.Fatalf("failed unit should not be verified: %s", out.String())
		}
	})

	t.Run("timer implicit unit", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"verify", timer}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit %d stdout=%s", code, out.String())
		}
		if !strings.Contains(out.String(), "foo.timer: verified") {
			t.Fatalf("stdout=%s", out.String())
		}
	})

	t.Run("missing path", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"verify"}, &out, &errb)
		if code != 2 {
			t.Fatalf("exit %d", code)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"verify", filepath.Join(dir, "absent.service")}, &out, &errb)
		if code != 1 {
			t.Fatalf("exit %d stdout=%s stderr=%s", code, out.String(), errb.String())
		}
		if !strings.Contains(errb.String(), "absent.service") {
			t.Fatalf("stderr=%s", errb.String())
		}
	})
}

func TestUnimplementedStillZero(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"start", "foo"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "not implemented") {
		t.Fatalf("stderr=%s", errb.String())
	}
}
