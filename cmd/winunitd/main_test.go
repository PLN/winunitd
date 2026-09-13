package main

import (
	"bytes"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/version"
)

func TestRunHelp(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--help"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, `\\.\pipe\winunitd\control`) {
		t.Fatalf("help missing pipe: %s", got)
	}
	if !strings.Contains(got, "--user-manager") {
		t.Fatalf("help missing --user-manager: %s", got)
	}
	if !strings.Contains(got, `\\.\pipe\winunitd\user\`) {
		t.Fatalf("help missing user pipe: %s", got)
	}
	if !strings.Contains(got, "install") || !strings.Contains(got, "uninstall") {
		t.Fatalf("help missing install/uninstall: %s", got)
	}
	if !strings.Contains(got, runtime.ServiceName) || !strings.Contains(got, runtime.DisplayName) {
		t.Fatalf("help missing SCM identity: %s", got)
	}
	if !strings.Contains(got, "Automatic, restart on failure") {
		t.Fatalf("help missing startup/recovery policy: %s", got)
	}
	if !strings.Contains(got, "linger") {
		t.Fatalf("help missing linger: %s", got)
	}
	if !strings.Contains(got, "default.target") {
		t.Fatalf("help missing boot target: %s", got)
	}
	if !strings.Contains(got, "graphical-session.target") {
		t.Fatalf("help missing graphical-session.target: %s", got)
	}
	if !strings.Contains(got, "--version") {
		t.Fatalf("help missing --version: %s", got)
	}
}

func TestRunVersion(t *testing.T) {
	for _, flag := range []string{"--version", "-version"} {
		var out, errb bytes.Buffer
		code := run([]string{flag}, &out, &errb)
		if code != 0 {
			t.Fatalf("%s exit %d stderr=%s", flag, code, errb.String())
		}
		want := "winunitd " + version.Version
		if !strings.Contains(out.String(), want) {
			t.Fatalf("%s stdout=%q want %q", flag, out.String(), want)
		}
		if version.Version != "0.1.0-alpha" {
			t.Fatalf("version.Version = %q, want 0.1.0-alpha", version.Version)
		}
	}
}

func TestRunUnexpectedArg(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"serve"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestRunInstallUnexpectedArg(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"install", "extra"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
}

func TestRunInstallUninstallOffWindows(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("install talks to SCM on Windows")
	}
	var out, errb bytes.Buffer
	code := run([]string{"install"}, &out, &errb)
	if code != 1 {
		t.Fatalf("install exit %d stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "Windows") {
		t.Fatalf("install stderr=%s", errb.String())
	}
	errb.Reset()
	code = run([]string{"uninstall"}, &out, &errb)
	if code != 1 {
		t.Fatalf("uninstall exit %d stderr=%s", code, errb.String())
	}
}

func TestRunUserManagerInvalidSID(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--user-manager", "not-a-sid"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
}
