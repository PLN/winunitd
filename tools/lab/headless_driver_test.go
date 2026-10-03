package main

import (
	"bytes"
	"encoding/json"
	"errors"
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

// headlessPure returns the driver's functions that need no machine state,
// and its tested helpers, whose effects (files, children, the recorder) the
// tests supply.
func headlessPure(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(headlessDriver)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	start := strings.Index(script, "#region pure functions")
	helpers := strings.Index(script, "#region tested helpers")
	end := -1
	if helpers > 0 {
		if n := strings.Index(script[helpers:], "#endregion"); n > 0 {
			end = helpers + n
		}
	}
	if start < 0 || helpers < start || end < helpers {
		t.Fatal("the driver has no pure-function and tested-helper regions")
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
	for _, step := range []string{"New-Item -ItemType Directory -Path $caseDir", "Invoke-Icacls (@($caseDir", "Start-Transcript", "Set-Linger 'A' $false"} {
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
	// Every JSON file native code reads goes through the BOM-free writer,
	// every icacls result is checked, every child is registered, and no
	// interactive header is composed without its observed session.
	for _, forbidden := range []*regexp.Regexp{regexp.MustCompile(`(?i)Set-Content[^\n]*-Encoding\s+UTF8`), regexp.MustCompile(`(?i)ConvertTo-Json[^\n]*\|\s*Set-Content`),
		regexp.MustCompile(`(?i)Start-Process`), regexp.MustCompile(`Get-HeadlessToken \$caseInfo\.Mode`), regexp.MustCompile(`\$session\b`),
		regexp.MustCompile(`\.ReadLine\(\)`)} {
		if forbidden.MatchString(script) {
			t.Errorf("the driver matches %s", forbidden)
		}
	}
	if strings.Count(script, "icacls.exe") != 1 {
		t.Error("icacls runs outside its checked helper")
	}
	if !strings.Contains(script, "default { throw \"This driver has no procedure for $key at stage $Stage\" }") {
		t.Error("an unknown case does not fail")
	}
	finally := strings.Index(script, "} finally {\n\t# Undo exactly what this execution changed, its children first.")
	if finally < main {
		t.Fatal("no restoring finally after the main try")
	}
	for _, undo := range []string{"Stop-Children", "Invoke-Wts 'logoff'", "Start-Broker", "Set-Linger $r $true"} {
		if !strings.Contains(script[finally:], undo) {
			t.Errorf("the finally does not undo with %s", undo)
		}
	}
}

// JSON the driver writes for native code has no byte-order mark, so the
// fixture's strict decoder reads it; one with a mark is refused.
func TestHeadlessDriverWritesJSONWithoutBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observation.json")
	var done struct{ Written bool }
	runHeadless(t, fmt.Sprintf(`
$token = Get-HeadlessToken 'system' '' 0
$o = New-HeadlessObservation -Key 'H22/all' -Kind 'primary' -Result 'pass' -Token $token -ExecutionId 'x-22' -Sequence 9 -BootId 'boot-3-133' `+
		`-PasswordLogons 0 -RunnerId '' -Cleanup $true -Controls @() -Evidence ([ordered]@{ inventory = [ordered]@{ baseline = [ordered]@{ name = 'c5' }; at = 2 } }) -Detail 'détail'
Write-JsonFile %s $o
@{ written = $true } | ConvertTo-Json`, psQuote(path)), &done)
	data, err := os.ReadFile(path)
	if err != nil || !done.Written {
		t.Fatal(err)
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || data[0] != '{' {
		t.Fatalf("the file starts with % x", data[:3])
	}
	strict := func(b []byte) error {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		var o headless.Observation
		return dec.Decode(&o)
	}
	if err := strict(data); err != nil {
		t.Fatalf("strict read: %v", err)
	}
	if err := strict(append([]byte{0xef, 0xbb, 0xbf}, data...)); err == nil {
		t.Fatal("a byte-order mark was accepted")
	}
}

// Every record must be admitted: a refused, unwritten or duplicate record
// is a failure of the run.
func TestHeadlessDriverRecordingTail(t *testing.T) {
	root := t.TempDir()
	results := filepath.Join(root, "results")
	if err := os.Mkdir(results, 0o700); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Refused, Silent, Admitted, Duplicate struct {
			Next     int
			Failures []string
		}
	}
	runHeadless(t, fmt.Sprintf(`
$caseRoot, $results = %s, %s
$records = @([ordered]@{ Key = 'H13/A'; Kind = 'primary'; Token = (Get-HeadlessToken 's4u' 'S-1-5-21-1-2-3-1001' 0); Evidence = $null; Controls = @(); Observer = ''; Runner = '' })
$common = @{ Records = $records; Case = 'H13'; Sequence = 4; ExecutionId = 'x'; BootId = 'boot-1-2'; Passed = $true; Cleanup = $true; Detail = 'd'
	CaseRoot = $caseRoot; Results = $results; ObserverBoot = { 'unused' } }
$refused = Complete-HeadlessRun @common -Record { throw 'injected record-write refusal' }
$silent = Complete-HeadlessRun @common -Record { }
$admitted = Complete-HeadlessRun @common -Record { param($a) Set-Content -LiteralPath (Join-Path $results (Get-ResultFileName 'H13/A')) -Value '{}' }
$duplicate = Complete-HeadlessRun @common -Record { }
@{ refused = $refused; silent = $silent; admitted = $admitted; duplicate = $duplicate } | ConvertTo-Json -Depth 4`, psQuote(root), psQuote(results)), &got)
	switch {
	case len(got.Refused.Failures) != 1 || !strings.Contains(got.Refused.Failures[0], "injected record-write refusal"):
		t.Errorf("refused record: %+v", got.Refused)
	case len(got.Silent.Failures) != 1 || !strings.Contains(got.Silent.Failures[0], "the recorder wrote no record"):
		t.Errorf("unwritten record: %+v", got.Silent)
	case len(got.Admitted.Failures) != 0 || got.Admitted.Next != 5:
		t.Errorf("admitted record: %+v", got.Admitted)
	case len(got.Duplicate.Failures) != 1 || !strings.Contains(got.Duplicate.Failures[0], "already exists"):
		t.Errorf("duplicate record: %+v", got.Duplicate)
	}
}

// The driver's own recording tail, with the recorder refusing: the run
// fails, exits nonzero and reports no next sequence.
func TestHeadlessDriverRecordRefusalFailsTheRun(t *testing.T) {
	data, err := os.ReadFile(headlessDriver)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	start := strings.Index(script, "# Records: one observation per record, each admitted")
	if start < 0 {
		t.Fatal("no recording tail")
	}
	root := t.TempDir()
	body := "$ErrorActionPreference = 'Stop'\nSet-StrictMode -Version Latest\n" + headlessPure(t) + fmt.Sprintf(`
function Get-BootId { 'boot-1-2' }
function Invoke-Native { throw 'injected record-write refusal' }
$Fixture, $Admission, $CaseRoot, $Results = 'fixture', 'admission', %s, %s
$Case, $Sequence, $ExecutionId, $cleanup, $pairs = 'H13', 1, 'x', $true, @()
$checks = New-Object Collections.Generic.List[string]
$failures = New-Object Collections.Generic.List[string]
$records = New-Object Collections.Generic.List[object]
$records.Add([ordered]@{ Key = 'H13/A'; Kind = 'primary'; Token = (Get-HeadlessToken 's4u' 'S-1-5-21-1-2-3-1001' 0); Evidence = $null; Controls = @(); Observer = ''; Runner = '' })
`, psQuote(root), psQuote(root)) + script[start:]
	path := filepath.Join(t.TempDir(), "tail.ps1")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(headlessPowerShell(t), "-NoProfile", "-NonInteractive", "-File", path).Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() == 0 {
		t.Fatalf("the tail exited %v with %q", err, out)
	}
	if strings.Contains(string(out), "next-sequence") || !strings.Contains(string(out), "injected record-write refusal") {
		t.Fatalf("output %q", out)
	}
}

// Interactive headers name the account's observed or recorded session,
// including after a logoff, and are refused when none or several are seen.
func TestHeadlessDriverInteractiveHeaders(t *testing.T) {
	var got struct {
		Observed, AfterLogoff, Filtered, Receipt, S4U struct{ Session int }
		Errors                                        []string
	}
	runHeadless(t, `
$sid, $other = 'S-1-5-21-1-2-3-1002', 'S-1-5-21-1-2-3-1001'
$live = [pscustomobject]@{ sessions = @([pscustomobject]@{ at = 1 }, [pscustomobject]@{ at = 2; users = @([pscustomobject]@{ session = 3; sid = $sid }, [pscustomobject]@{ session = 4; sid = $other }) }) }
$loggedOff = [pscustomobject]@{ sessions = @([pscustomobject]@{ at = 1 }, [pscustomobject]@{ at = 2; users = @([pscustomobject]@{ session = 2; sid = $sid }) }, [pscustomobject]@{ at = 3 }) }
$receipt = [pscustomobject]@{ runner = [pscustomobject]@{ token = [pscustomobject]@{ sid = $sid; session = 5 } }; owner = [pscustomobject]@{ token = [pscustomobject]@{ sid = $sid; session = 5 } } }
$errors = @()
foreach ($bad in @(
		{ Get-RecordToken 'wts' $sid ([pscustomobject]@{ sessions = @([pscustomobject]@{ at = 1 }) }) },
		{ Get-RecordToken 'wts' $sid ([pscustomobject]@{ stage = 'finished' }) },
		{ Get-RecordToken 'wts' $sid ([pscustomobject]@{ sessions = @([pscustomobject]@{ at = 1; users = @([pscustomobject]@{ session = 2; sid = $sid }, [pscustomobject]@{ session = 6; sid = $sid }) }) }) },
		{ Get-RecordToken 'wts' $sid ([pscustomobject]@{ sessions = @([pscustomobject]@{ at = 1; users = @([pscustomobject]@{ session = 0; sid = $sid }) }) }) },
		{ Get-RecordToken 'filtered-admin' $sid },
		{ Get-RecordToken 'wts' $sid $null ([pscustomobject]@{ runner = [pscustomobject]@{ token = [pscustomobject]@{ sid = 'S-1-5-18'; session = 0 } }; owner = [pscustomobject]@{ token = [pscustomobject]@{ sid = $sid; session = 5 } } }) },
		{ Get-RecordToken 'wts' $sid $null ([pscustomobject]@{ runner = [pscustomobject]@{ token = [pscustomobject]@{ sid = $sid; session = 0 } }; owner = [pscustomobject]@{ token = [pscustomobject]@{ sid = $sid; session = 0 } } }) })) {
	try { & $bad | Out-Null; $errors += 'accepted' } catch { $errors += $_.Exception.Message }
}
@{ observed = (Get-RecordToken 'wts' $sid $live); afterLogoff = (Get-RecordToken 'wts' $sid $loggedOff); filtered = (Get-RecordToken 'filtered-admin' $sid $live)
	receipt = (Get-RecordToken 'wts' $sid $null $receipt); s4u = (Get-RecordToken 's4u' $sid $live); errors = $errors } | ConvertTo-Json -Depth 4
`, &got)
	if got.Observed.Session != 3 || got.AfterLogoff.Session != 2 || got.Filtered.Session != 3 || got.Receipt.Session != 5 || got.S4U.Session != 0 {
		t.Fatalf("headers %+v", got)
	}
	for i, e := range got.Errors {
		if e == "accepted" {
			t.Errorf("contradictory session evidence %d accepted", i)
		}
	}
}

// A registered child is stopped and reaped by the finally block after an
// injected failure; a reply that never comes fails within its deadline;
// a spaced path stays one argument.
func TestHeadlessDriverCustodyAndDeadlines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX sleep child")
	}
	var got struct {
		Exited      bool
		Problems    []string
		Reply       string
		Timeout     string
		ElapsedMs   int
		CommandLine string
	}
	runHeadless(t, `
$child = $null
$problems = @()
try {
	$child = Start-Child 'sleep' @('30')
	throw 'injected setup failure'
} catch { } finally { $problems = Stop-Children 200 }
$server = New-Object IO.Pipes.AnonymousPipeServerStream([IO.Pipes.PipeDirection]::In)
$client = New-Object IO.Pipes.AnonymousPipeClientStream([IO.Pipes.PipeDirection]::Out, $server.ClientSafePipeHandle)
$writer = New-Object IO.StreamWriter($client)
$writer.WriteLine('{"ok":true}'); $writer.Flush()
$reader = New-Object IO.StreamReader($server)
$reply = Read-LineWithin $reader 2000
$watch = [Diagnostics.Stopwatch]::StartNew()
$timeout = ''
try { Read-LineWithin $reader 300 | Out-Null } catch { $timeout = $_.Exception.Message }
@{ exited = $child.HasExited; problems = $problems; reply = $reply; timeout = $timeout; elapsedMs = [int]$watch.ElapsedMilliseconds
	commandLine = (Format-NativeCommandLine @('pipe-serve', '--report', 'C:\case dir\server.json', '--name', '\\.\pipe\winunitd-qual\h15')) } | ConvertTo-Json
`, &got)
	if !got.Exited || len(got.Problems) != 1 || !strings.Contains(got.Problems[0], "killed after its deadline") {
		t.Errorf("child custody: exited=%t problems=%q", got.Exited, got.Problems)
	}
	if got.Reply != `{"ok":true}` || !strings.Contains(got.Timeout, "No reply within 300 ms") || got.ElapsedMs > 5000 {
		t.Errorf("reply %q timeout %q after %d ms", got.Reply, got.Timeout, got.ElapsedMs)
	}
	if got.CommandLine != `pipe-serve --report "C:\case dir\server.json" --name \\.\pipe\winunitd-qual\h15` {
		t.Errorf("command line %s", got.CommandLine)
	}
}

// An H20 repetition is one recorder running the three held-launch tests,
// one record each, under the repetition's runner label; the driver runs
// it as one execution and refuses the single-variant form.
func TestHeadlessDriverNativeTestGroup(t *testing.T) {
	var got struct {
		Runner        string
		Keys, Tests   []string
		BadRepetition string
	}
	runHeadless(t, `
$g = Get-NativeTestGroup 'r3'
$bad = ''
try { Get-NativeTestGroup 'r9' | Out-Null } catch { $bad = $_.Exception.Message }
@{ runner = $g.Runner; keys = $g.Keys; tests = $g.Tests; badRepetition = $bad } | ConvertTo-Json`, &got)
	if got.Runner != "runner-r3" || strings.Join(got.Keys, " ") != "H20/revocation/r3 H20/shutdown/r3 H20/deadline/r3" || len(got.Tests) != 3 || got.BadRepetition == "" {
		t.Fatalf("group %+v", got)
	}
	m, err := headless.CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	for i, key := range got.Keys {
		found := false
		for _, e := range m.Expand() {
			if e.Key == key && e.Test == got.Tests[i] {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not run its matrix test", key)
		}
	}
	data, err := os.ReadFile(headlessDriver)
	if err != nil {
		t.Fatal(err)
	}
	branch := string(data)
	start := strings.Index(branch, "'^H20/group/run/$' {")
	end := strings.Index(branch, "'^H20/' { throw")
	if start < 0 || end < start {
		t.Fatal("no grouped H20 procedure, or the single-variant form is not refused")
	}
	branch = branch[start:end]
	if strings.Count(branch, "Invoke-NativeTestGroup") != 1 || strings.Count(branch, "Invoke-NativeTestRecorder") != 1 {
		t.Error("the H20 repetition is not one recorder running every test")
	}
	// The driver's grouping path with the recorder mocked: one recorder
	// call names every test, and each record carries its own receipt, the
	// repetition's runner label and its own execution ID.
	dir := t.TempDir()
	var run struct {
		Calls   int
		Args    []string
		Records []struct {
			Key, Runner, ExecutionId string
			Token                    struct{ Session int }
			Evidence                 struct{ TestRun struct{ Artifact string } }
		}
	}
	runHeadless(t, fmt.Sprintf(`
$calls = 0
$seen = @()
$records = Invoke-NativeTestGroup (Get-NativeTestGroup 'r4') %s 'S-1-5-21-1-2-3-1001' 'x-h20-r4' 'C:	ools	est2json.exe' 'C:	ools
untime.test.exe' {
	param([string[]]$Arguments)
	$script:calls++
	$script:seen = $Arguments
	$dir = $Arguments[[array]::IndexOf($Arguments, '--out-dir') + 1]
	for ($i = 0; $i -lt $Arguments.Count; $i++) {
		if ($Arguments[$i] -eq '--each') { Write-JsonFile (Join-Path $dir "$($Arguments[$i + 1]).json") @{ artifact = $Arguments[$i + 1] } }
	}
}
@{ calls = $calls; args = $seen; records = $records } | ConvertTo-Json -Depth 6`, psQuote(dir)), &run)
	if run.Calls != 1 || !strings.Contains(strings.Join(run.Args, " "), "--runner runner-r4") || !strings.Contains(strings.Join(run.Args, " "), "--subjects") ||
		strings.Count(strings.Join(run.Args, " "), "--each") != 3 || len(run.Records) != 3 {
		t.Fatalf("grouping run %+v", run)
	}
	for i, r := range run.Records {
		want := strings.Replace(got.Keys[i], "/r3", "/r4", 1)
		if r.Key != want || r.Runner != "runner-r4" || r.ExecutionId != "x-h20-r4-"+strings.Split(want, "/")[1] ||
			r.Evidence.TestRun.Artifact != got.Tests[i] || r.Token.Session != 0 {
			t.Errorf("record %d %+v", i, r)
		}
	}
}
