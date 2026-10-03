package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const nestedJobDriver = "assets/nested-job-checks.ps1"

func nestedJobPowerShell(t *testing.T) string {
	t.Helper()
	exe, err := exec.LookPath("pwsh")
	if err != nil {
		exe, err = exec.LookPath("powershell")
	}
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Fatal("powershell is required to check the nested-job driver")
		}
		t.Skip("powershell is not installed")
	}
	return exe
}

// pureRegion returns the driver's functions that need no machine state.
func pureRegion(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(nestedJobDriver)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	start := strings.Index(script, "#region pure functions")
	end := strings.Index(script, "#endregion")
	if start < 0 || end < start {
		t.Fatal("the driver has no pure-function region")
	}
	return script[start:end]
}

// runDriverFunctions runs body after the driver's pure functions and
// decodes the JSON it prints.
func runDriverFunctions(t *testing.T, body string, out any) error {
	t.Helper()
	script := "$ErrorActionPreference = 'Stop'\nSet-StrictMode -Version Latest\n" + pureRegion(t) + "\n" + body
	path := filepath.Join(t.TempDir(), "driver-functions.ps1")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := exec.Command(nestedJobPowerShell(t), "-NoProfile", "-NonInteractive", "-File", path).Output()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

type driverLayout struct {
	Name       string
	CaseDir    string
	UnitDir    string
	UnitPath   string
	EnablePath *string
}

// A SYSTEM case needs no headless account or manager directory: its
// parameters pass and its layout has no enable link (the default path once
// failed on an empty HeadlessBase before running its case).
func TestNestedJobDriverSystemLayoutNeedsNoHeadlessArguments(t *testing.T) {
	var layout driverLayout
	err := runDriverFunctions(t, `
Test-NestedJobParameters -Case N07 -Identity system -HeadlessSid '' -HeadlessAccount '' -HeadlessBase '' -Paths @('/fixture/nested-job.exe', '/cases', '/data')
Get-NestedJobLayout -Case N07 -Mode job-list -Identity system -Repetition r1 -CaseRoot '/cases' -DataDir '/data' -HeadlessBase '' | ConvertTo-Json -Compress
`, &layout)
	if err != nil {
		t.Fatal(err)
	}
	if layout.Name != "nested-n07-job-list-r1.service" || layout.EnablePath != nil || !strings.HasSuffix(layout.UnitPath, "units/nested-n07-job-list-r1.service") ||
		!strings.HasPrefix(layout.UnitDir, "/data") || !strings.HasSuffix(layout.CaseDir, "n07-job-list-system-r1") {
		t.Fatalf("SYSTEM layout %+v", layout)
	}
}

func TestNestedJobDriverHeadlessLayout(t *testing.T) {
	var layout driverLayout
	err := runDriverFunctions(t, `
Test-NestedJobParameters -Case N06 -Identity headless -HeadlessSid 'S-1-5-21-1-2-3-1001' -HeadlessAccount 'qualifier' -HeadlessBase '/profile/winunitd' -Paths @('/fixture/nested-job.exe', '/cases', '/data')
Get-NestedJobLayout -Case N06 -Mode assign -Identity headless -Repetition r2 -CaseRoot '/cases' -DataDir '/data' -HeadlessBase '/profile/winunitd' | ConvertTo-Json -Compress
`, &layout)
	if err != nil {
		t.Fatal(err)
	}
	if layout.EnablePath == nil || !strings.HasPrefix(*layout.EnablePath, "/profile/winunitd") ||
		!strings.HasSuffix(*layout.EnablePath, "default.target/nested-n06-assign-r2.service") || !strings.HasPrefix(layout.UnitDir, "/profile/winunitd") {
		t.Fatalf("headless layout %+v", layout)
	}
}

func TestNestedJobDriverRejectsInvalidParameters(t *testing.T) {
	for name, call := range map[string]string{
		"headless without a manager directory": `Test-NestedJobParameters -Case N06 -Identity headless -HeadlessSid 'S-1-5-21-1-2-3-1001' -HeadlessAccount 'qualifier' -HeadlessBase '' -Paths @('/cases')`,
		"headless without an account":          `Test-NestedJobParameters -Case N07 -Identity headless -HeadlessSid '' -HeadlessAccount '' -HeadlessBase '/profile' -Paths @('/cases')`,
		"N06 as SYSTEM":                        `Test-NestedJobParameters -Case N06 -Identity system -HeadlessSid '' -HeadlessAccount '' -HeadlessBase '' -Paths @('/cases')`,
		"relative path":                        `Test-NestedJobParameters -Case N07 -Identity system -HeadlessSid '' -HeadlessAccount '' -HeadlessBase '' -Paths @('cases')`,
		"empty path":                           `Test-NestedJobParameters -Case N07 -Identity system -HeadlessSid '' -HeadlessAccount '' -HeadlessBase '' -Paths @('')`,
	} {
		var ignored any
		if err := runDriverFunctions(t, call+"\n'null'", &ignored); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// A process the driver kills counts as stopped only once its exit is
// confirmed.
func TestNestedJobDriverConfirmsKilledProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX sleep process")
	}
	var got struct {
		State  string
		Exited bool
	}
	err := runDriverFunctions(t, `
$p = Start-Process -FilePath 'sleep' -ArgumentList '60' -PassThru
$state = Stop-ExactProcess $p 'the command'
@{ State = $state; Exited = $p.HasExited } | ConvertTo-Json -Compress
`, &got)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exited || got.State != "the command was killed after its deadline" {
		t.Fatalf("kill %+v", got)
	}
}

// Setup that changes the machine runs inside the driver's try, so its
// finally always runs; the only kill goes through Stop-ExactProcess.
func TestNestedJobDriverStructure(t *testing.T) {
	data, err := os.ReadFile(nestedJobDriver)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	main := strings.Index(script, "\ntry {\n\tif (Test-Path -LiteralPath $caseDir)")
	if main < 0 {
		t.Fatal("the case setup is not inside the main try")
	}
	for _, step := range []string{"New-Item -ItemType Directory -Path $caseDir", "icacls.exe", "Start-Transcript"} {
		if i := strings.Index(script, step); i < main {
			t.Errorf("%s runs before the main try", step)
		}
	}
	if strings.Count(script, ".Kill()") != 1 || strings.Count(script, "Stop-ExactProcess") < 3 {
		t.Error("a process is killed without confirming its exit")
	}
	// Only the layout function builds paths on the headless manager
	// directory, and only for headless cases.
	outside := strings.Replace(script, pureRegion(t), "", 1)
	if strings.Contains(outside, "Join-Path $HeadlessBase") || strings.Contains(outside, "Join-Path (Join-Path $HeadlessBase") {
		t.Error("a path is built on the headless manager directory outside the layout")
	}
}
