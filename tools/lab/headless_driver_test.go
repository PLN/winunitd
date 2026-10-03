package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/runtime/runtimetest/headless"
)

const headlessDriver = "assets/headless-workload-checks.ps1"

func headlessPowerShell(t *testing.T) string {
	t.Helper()
	exe, err := exec.LookPath("pwsh")
	if err != nil {
		exe, err = exec.LookPath("powershell")
	}
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Fatal("powershell is required to check the headless driver")
		}
		t.Skip("powershell is not installed")
	}
	return exe
}

// headlessPure returns the driver's functions that need no machine state.
func headlessPure(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(headlessDriver)
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

// runHeadless runs body after the driver's pure functions and decodes the
// JSON it prints.
func runHeadless(t *testing.T, body string, out any) {
	t.Helper()
	script := "$ErrorActionPreference = 'Stop'\nSet-StrictMode -Version Latest\n" + headlessPure(t) + "\n" + body
	path := filepath.Join(t.TempDir(), "headless-functions.ps1")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := exec.Command(headlessPowerShell(t), "-NoProfile", "-NonInteractive", "-File", path).Output()
	if err != nil {
		t.Fatalf("driver functions: %v", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("driver output: %v", err)
	}
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// The driver's account and mode for every executed record and control of
// the matrix are the matrix's own.
func TestHeadlessDriverCaseTableMatchesTheMatrix(t *testing.T) {
	m, err := headless.CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		Case, Variant, Control, Account, Mode string
		Phase                                 int
	}
	var want []row
	var calls strings.Builder
	calls.WriteString("$rows = @()\n")
	for _, e := range m.Expand() {
		if e.Plane == headless.PlaneReference {
			continue
		}
		want = append(want, row{e.Case, e.Variant, "", e.Account, e.Mode, e.Phase})
		fmt.Fprintf(&calls, "$rows += Get-HeadlessCase %s %s ''\n", psQuote(e.Case), psQuote(e.Variant))
	}
	calls.WriteString("ConvertTo-Json -InputObject $rows -Depth 4\n")
	var got []struct {
		Case, Variant, Account, Mode string
		Needs, Stages                []string
	}
	runHeadless(t, calls.String(), &got)
	if len(got) != len(want) {
		t.Fatalf("%d rows for %d records", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		account := map[string]string{headless.AccountA: "A", headless.AccountB: "B", headless.AccountAdmin: "admin", headless.AccountSystem: "system"}[w.Account]
		if g.Account != account || g.Mode != w.Mode {
			t.Errorf("%s/%s: driver %s %s, matrix %s %s", w.Case, w.Variant, g.Account, g.Mode, w.Account, w.Mode)
		}
		if len(g.Needs) < 5 || len(g.Stages) == 0 {
			t.Errorf("%s/%s: needs %v stages %v", w.Case, w.Variant, g.Needs, g.Stages)
		}
		// The first two phases allow no password-bearing logon: nothing
		// there may need a password runner, a session or a WTS logon.
		if w.Phase <= 2 {
			for _, n := range g.Needs {
				if n == "password-runner" || n == "session-runner" || n == "wts-client" {
					t.Errorf("%s/%s in phase %d needs %s", w.Case, w.Variant, w.Phase, n)
				}
			}
		}
	}
}

func TestHeadlessDriverPrerequisites(t *testing.T) {
	var got struct {
		Needs   []string
		Missing []string
	}
	runHeadless(t, `
$c = Get-HeadlessCase 'H18' 'A' 'password-share'
$provided = @{ Fixture = '/f'; Admission = '/a'; CaseRoot = '/c'; Results = '/r'; SidA = 'S-1-5-21-1-2-3-1001'; SidB = 'S-1-5-21-1-2-3-1002'
	AccountA = 'a'; AccountB = 'b'; BaseA = '/ba'; BaseB = '/bb'; SmbServer = 'peer'; SmbPath = '\\peer\share\n.txt' }
@{ needs = $c.Needs; missing = (Get-MissingPrerequisites -Needs $c.Needs -Provided $provided) } | ConvertTo-Json
`, &got)
	if strings.Join(got.Missing, ";") != "smb-share (SmbSha256);password-runner (PasswordRunner)" {
		t.Fatalf("missing %q", got.Missing)
	}
	for _, c := range []struct{ kase, variant, control, need string }{
		{"G6", "filtered-admin", "", "admin-account"}, {"G1", "B", "", "wts-client"}, {"H17", "A", "peer-receipt", "peer-receipt"},
		{"H18", "B", "server-principal", "peer-audit"}, {"H20", "revocation", "", "test2json"}, {"H22", "all", "", "baseline"}, {"H22", "all", "", "admin-account"},
		{"G6", "go-tests", "", "session-runner"}, {"H01", "A", "", "baseline"},
	} {
		var needs struct{ Needs []string }
		runHeadless(t, fmt.Sprintf("Get-HeadlessCase %s %s %s | ConvertTo-Json", psQuote(c.kase), psQuote(c.variant), psQuote(c.control)), &needs)
		if !strings.Contains(strings.Join(needs.Needs, ","), c.need) {
			t.Errorf("%s/%s#%s needs %v, not %s", c.kase, c.variant, c.control, needs.Needs, c.need)
		}
	}
}

func TestHeadlessDriverKeysAndPlans(t *testing.T) {
	var got struct {
		Keys                    []string
		G1, G2, G5, H09, Others []string
	}
	runHeadless(t, `
@{ keys = @((Get-HeadlessKey 'H20' 'shutdown' 'r3' ''), (Get-HeadlessKey 'H18' 'A' '' 'password-share'), (Get-HeadlessKey 'G6' 'go-tests' '' ''))
	g1 = @(Get-ObserverPlan 'G1' 'B' 'B' 'wts'); g2 = @(Get-ObserverPlan 'G2' 'A' 'A'); g5 = @(Get-ObserverPlan 'G5' 'A' 'A' 's4u' '/f/release-A')
	h09 = @(Get-ObserverPlan 'H09' 'A' 'A'); others = @(Get-ObserverPlan 'H13' 'A' 'A') } | ConvertTo-Json -Depth 3
`, &got)
	if strings.Join(got.Keys, " ") != "H20/shutdown/r3 H18/A#password-share G6/go-tests" {
		t.Fatalf("keys %q", got.Keys)
	}
	plan := func(p []string) string { return strings.Join(p, " ") }
	switch {
	case !strings.Contains(plan(got.G1), "--target B --crash manager --crash-class wts"):
		t.Errorf("G1 plan %q", plan(got.G1))
	case !strings.Contains(plan(got.G2), "--crash-class s4u"):
		t.Errorf("G2 plan %q", plan(got.G2))
	case !strings.Contains(plan(got.G5), "--release-file /f/release-A --release-after 410"):
		t.Errorf("G5 plan %q", plan(got.G5))
	case strings.Contains(plan(got.H09), "--target") || !strings.Contains(plan(got.H09), "--crash broker"):
		t.Errorf("H09 plan %q", plan(got.H09))
	case strings.Contains(plan(got.Others), "--crash"):
		t.Errorf("probe plan %q", plan(got.Others))
	}
}

// The driver's observation is what the record command reads, strictly, and
// the record binds it.
func TestHeadlessDriverObservationsRecord(t *testing.T) {
	var raw json.RawMessage
	runHeadless(t, `
$token = Get-HeadlessToken 'system' '' 0
$o = New-HeadlessObservation -Key 'H22/all' -Kind 'primary' -Result 'pass' -Token $token -ExecutionId 'x-22' -Sequence 9 -BootId 'boot-3-133' `+
		`-PasswordLogons 0 -RunnerId '' -Cleanup $true -Controls @() -Evidence ([ordered]@{ inventory = [ordered]@{ baseline = [ordered]@{ name = 'c5'; at = 1 }; at = 2 } }) -Detail 'checks'
ConvertTo-Json -InputObject $o -Depth 8
`, &raw)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var o headless.Observation
	if err := dec.Decode(&o); err != nil {
		t.Fatal(err)
	}
	if o.Token == nil || o.Token.SID != headless.SystemSID || o.Sequence != 9 || o.Evidence.Inventory == nil || o.Evidence.Inventory.Baseline.Name != "c5" {
		t.Fatalf("observation %+v", o)
	}
	m, err := headless.CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	adm, _ := json.Marshal(headless.Admission{Schema: headless.AdmissionSchema, Source: strings.Repeat("a", 40),
		Artifacts: []headless.Artifact{{Name: "headless-workload.exe", SHA256: strings.Repeat("b", 64)}}})
	run, err := headless.DecodeAdmission(adm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := headless.BuildRecord(m, o, nil, run, strings.Repeat("b", 64)); err != nil {
		t.Fatalf("record: %v", err)
	}
	var peer struct {
		Source  string
		SID     *string `json:"sid"`
		Session int
	}
	runHeadless(t, "Get-HeadlessToken 'peer' '' 0 | ConvertTo-Json", &peer)
	if peer.Source != "peer" || peer.SID != nil {
		t.Fatalf("peer token %+v", peer)
	}
}

// The driver lists a case's lab prerequisites without touching the
// machine, so the lab can prepare them.
func TestHeadlessDriverListsPrerequisites(t *testing.T) {
	out, err := exec.Command(headlessPowerShell(t), "-NoProfile", "-NonInteractive", "-File", headlessDriver, "-Case", "H19", "-Variant", "B",
		"-Control", "password-decrypt", "-ListPrerequisites").Output()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Account, Mode  string
		Needs, Missing []string
		Stages         []string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Account != "B" || got.Mode != "s4u" || !strings.Contains(strings.Join(got.Needs, ","), "efs-fixture,password-runner") || len(got.Missing) == 0 {
		t.Fatalf("listing %+v", got)
	}
	// The cold boot's baseline receipt is its own stage, before the lab
	// provisions anything.
	var cold struct{ Stages []string }
	runHeadless(t, "Get-HeadlessCase 'H01' 'A' | ConvertTo-Json", &cold)
	if strings.Join(cold.Stages, ",") != "baseline,prepare,collect" {
		t.Fatalf("H01 stages %v", cold.Stages)
	}
}

// Machine changes run inside the main try so its finally restores them;
// the only kill confirms the exit; nothing reboots, stores a password or
// builds a path on an account base outside its helpers.
func TestHeadlessDriverStructure(t *testing.T) {
	data, err := os.ReadFile(headlessDriver)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	main := strings.Index(script, "\ntry {\n\tif (Test-Path -LiteralPath $caseDir)")
	if main < 0 {
		t.Fatal("the case setup is not inside the main try")
	}
	for _, step := range []string{"New-Item -ItemType Directory -Path $caseDir", "icacls.exe $caseDir", "Start-Transcript", "Set-Linger 'A' $false"} {
		if i := strings.Index(script, step); i < main {
			t.Errorf("%s runs before the main try", step)
		}
	}
	if strings.Count(script, ".Kill()") != 1 {
		t.Error("a process is killed without confirming its exit")
	}
	for _, forbidden := range []*regexp.Regexp{regexp.MustCompile(`(?i)restart-computer`), regexp.MustCompile(`(?i)convertto-securestring`),
		regexp.MustCompile(`(?i)get-credential`), regexp.MustCompile(`(?i)\$password\b`), regexp.MustCompile(`(?i)-password\b`)} {
		if forbidden.MatchString(script) {
			t.Errorf("the driver matches %s", forbidden)
		}
	}
	if !strings.Contains(script, "default { throw \"This driver has no procedure for $key at stage $Stage\" }") {
		t.Error("an unknown case does not fail")
	}
	finally := strings.Index(script, "} finally {\n\t# Undo exactly what this execution changed.")
	if finally < main {
		t.Fatal("no restoring finally after the main try")
	}
	for _, undo := range []string{"Invoke-Wts 'logoff'", "Start-Broker", "Set-Linger $r $true"} {
		if !strings.Contains(script[finally:], undo) {
			t.Errorf("the finally does not undo with %s", undo)
		}
	}
}
