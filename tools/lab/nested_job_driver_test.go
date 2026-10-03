package main

import (
	"encoding/json"
	"fmt"
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

// psLiteral quotes s as a PowerShell single-quoted string.
func psLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// driverRoots returns host-native absolute roots for the layout functions:
// the fixture, case root, data directory and headless manager directory.
// The roots contain a quote character, so a test fails if they are not
// passed as PowerShell literals.
func driverRoots(t *testing.T) (fixture, cases, data, base string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "qualifier's root")
	return filepath.Join(root, "fixture", "nested-job.exe"), filepath.Join(root, "cases"), filepath.Join(root, "data"), filepath.Join(root, "profile", "winunitd")
}

// samePath compares a path PowerShell built with the host-native spelling.
func samePath(got, want string) bool {
	return filepath.Clean(got) == filepath.Clean(want)
}

// A SYSTEM case needs no headless account or manager directory: its
// parameters pass and its layout has no enable link (the default path once
// failed on an empty HeadlessBase before running its case).
func TestNestedJobDriverSystemLayoutNeedsNoHeadlessArguments(t *testing.T) {
	fixture, cases, data, _ := driverRoots(t)
	var layout driverLayout
	err := runDriverFunctions(t, fmt.Sprintf(`
Test-NestedJobParameters -Case N07 -Identity system -HeadlessSid '' -HeadlessAccount '' -HeadlessBase '' -Paths @(%s, %s, %s)
Get-NestedJobLayout -Case N07 -Mode job-list -Identity system -Repetition r1 -CaseRoot %s -DataDir %s -HeadlessBase '' | ConvertTo-Json -Compress
`, psLiteral(fixture), psLiteral(cases), psLiteral(data), psLiteral(cases), psLiteral(data)), &layout)
	if err != nil {
		t.Fatal(err)
	}
	const name = "nested-n07-job-list-r1.service"
	if layout.Name != name || layout.EnablePath != nil ||
		!samePath(layout.CaseDir, filepath.Join(cases, "n07-job-list-system-r1")) ||
		!samePath(layout.UnitDir, filepath.Join(data, "units")) ||
		!samePath(layout.UnitPath, filepath.Join(data, "units", name)) {
		t.Fatalf("SYSTEM layout %+v", layout)
	}
}

func TestNestedJobDriverHeadlessLayout(t *testing.T) {
	fixture, cases, data, base := driverRoots(t)
	var layout driverLayout
	err := runDriverFunctions(t, fmt.Sprintf(`
Test-NestedJobParameters -Case N06 -Identity headless -HeadlessSid 'S-1-5-21-1-2-3-1001' -HeadlessAccount 'qualifier' -HeadlessBase %s -Paths @(%s, %s, %s)
Get-NestedJobLayout -Case N06 -Mode assign -Identity headless -Repetition r2 -CaseRoot %s -DataDir %s -HeadlessBase %s | ConvertTo-Json -Compress
`, psLiteral(base), psLiteral(fixture), psLiteral(cases), psLiteral(data), psLiteral(cases), psLiteral(data), psLiteral(base)), &layout)
	if err != nil {
		t.Fatal(err)
	}
	const name = "nested-n06-assign-r2.service"
	if layout.Name != name || layout.EnablePath == nil ||
		!samePath(*layout.EnablePath, filepath.Join(base, "enabled", "default.target", name)) ||
		!samePath(layout.CaseDir, filepath.Join(cases, "n06-assign-headless-r2")) ||
		!samePath(layout.UnitDir, filepath.Join(base, "units")) ||
		!samePath(layout.UnitPath, filepath.Join(base, "units", name)) {
		t.Fatalf("headless layout %+v", layout)
	}
}

func TestNestedJobDriverRejectsInvalidParameters(t *testing.T) {
	_, cases, _, base := driverRoots(t)
	c, b := psLiteral(cases), psLiteral(base)
	for name, call := range map[string]string{
		"headless without a manager directory": `Test-NestedJobParameters -Case N06 -Identity headless -HeadlessSid 'S-1-5-21-1-2-3-1001' -HeadlessAccount 'qualifier' -HeadlessBase '' -Paths @(` + c + `)`,
		"headless without an account":          `Test-NestedJobParameters -Case N07 -Identity headless -HeadlessSid '' -HeadlessAccount '' -HeadlessBase ` + b + ` -Paths @(` + c + `)`,
		"N06 as SYSTEM":                        `Test-NestedJobParameters -Case N06 -Identity system -HeadlessSid '' -HeadlessAccount '' -HeadlessBase '' -Paths @(` + c + `)`,
		"relative path":                        `Test-NestedJobParameters -Case N07 -Identity system -HeadlessSid '' -HeadlessAccount '' -HeadlessBase '' -Paths @('cases')`,
		"empty path":                           `Test-NestedJobParameters -Case N07 -Identity system -HeadlessSid '' -HeadlessAccount '' -HeadlessBase '' -Paths @('')`,
		"relative manager directory":           `Test-NestedJobParameters -Case N07 -Identity headless -HeadlessSid 'S-1-5-21-1-2-3-1001' -HeadlessAccount 'qualifier' -HeadlessBase 'profile' -Paths @(` + c + `)`,
	} {
		var ignored any
		if err := runDriverFunctions(t, call+"\n'null'", &ignored); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// The same parameters with valid values pass, so each rejection above
	// is its own.
	var ok any
	if err := runDriverFunctions(t, `Test-NestedJobParameters -Case N07 -Identity headless -HeadlessSid 'S-1-5-21-1-2-3-1001' -HeadlessAccount 'qualifier' -HeadlessBase `+b+` -Paths @(`+c+`)`+"\n'null'", &ok); err != nil {
		t.Fatalf("valid headless parameters: %v", err)
	}
}

// A process the driver kills counts as stopped only once its exit is
// confirmed. The child is this PowerShell's own executable, sleeping, so
// the test runs the same way on every system.
func TestNestedJobDriverConfirmsKilledProcesses(t *testing.T) {
	var got struct {
		State  string
		Exited bool
	}
	err := runDriverFunctions(t, `
$p = Start-Process -FilePath (Get-Process -Id $PID).Path -ArgumentList '-NoProfile -NonInteractive -Command Start-Sleep -Seconds 60' -NoNewWindow -PassThru
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
