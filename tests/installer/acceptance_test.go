package installer_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/version"
)

// requiredCases is the R6.4 acceptance matrix. A skipped case that needs
// claimed media or an older MSI stays deferred. For 0.1-alpha, R6.1 and
// R6.4 are checked when the runnable set has passed on at least one
// guest, including an unclaimed Eval guest. deferred-media and
// deferred-older-msi stay listed. R6.5 stays open.
var requiredCases = []string{
	"quiet-install",
	"gui-install",
	"system-install",
	"offline-install",
	"repair-fa",
	"repair-reinstall",
	"uninstall",
	"reinstall-retained",
	"downgrade",
	"n1-upgrade",
	"locked-file",
	"rollback-test-fail-running",
	"rollback-test-fail-stopped",
	"rollback-upgrade",
	"non-admin",
	"beta-conflict",
	"preflight-reparse",
	"preflight-unmanaged-service",
}

func TestAcceptanceMatrixMatchesHarness(t *testing.T) {
	root := moduleRoot(t)
	var matrix acceptanceMatrix
	decode(t, root, "tests/installer/acceptance-matrix.json", &matrix)
	if matrix.Schema != 1 {
		t.Fatalf("schema = %d", matrix.Schema)
	}
	script := readRepo(t, root, "tests/installer/acceptance.ps1")
	evidence := readRepo(t, root, "docs/R6-EVIDENCE.md")
	milestones := readRepo(t, root, "docs/MILESTONES.md")
	wxs := readRepo(t, root, "packaging/wix/Package.wxs")

	got := map[string]acceptanceCase{}
	for _, item := range matrix.Cases {
		if _, ok := got[item.ID]; ok {
			t.Fatalf("duplicate case %s", item.ID)
		}
		got[item.ID] = item
	}
	if len(got) != len(requiredCases) {
		t.Fatalf("matrix has %d cases, want %d", len(got), len(requiredCases))
	}
	labels := regexp.MustCompile(`(?m)^\t\t'([a-z0-9-]+)' \{`).FindAllStringSubmatch(script, -1)
	implemented := map[string]bool{}
	for _, label := range labels {
		implemented[label[1]] = true
	}
	for _, id := range requiredCases {
		item, ok := got[id]
		if !ok {
			t.Fatalf("matrix is missing %s", id)
		}
		if !implemented[id] {
			t.Fatalf("harness does not implement %s", id)
		}
		if !strings.Contains(evidence, id) {
			t.Fatalf("R6 evidence does not list %s", id)
		}
		if len(item.ExpectExit) == 0 {
			t.Fatalf("%s has no expected exit", id)
		}
		for _, code := range item.ExpectExit {
			if code == 3010 {
				t.Fatalf("%s treats exit 3010 as a pass", id)
			}
		}
		switch item.Requires.Identity {
		case "elevated", "system", "not-elevated":
		default:
			t.Fatalf("%s identity %q", id, item.Requires.Identity)
		}
		switch item.Requires.Product {
		case "absent", "present", "any":
		default:
			t.Fatalf("%s product %q", id, item.Requires.Product)
		}
		for _, gate := range item.Gate {
			switch gate {
			case "R6.1", "R6.3", "R6.4":
			default:
				t.Fatalf("%s gate %s", id, gate)
			}
		}
	}
	for _, id := range []string{"quiet-install", "gui-install", "repair-fa", "repair-reinstall", "uninstall"} {
		if !hasGate(got[id], "R6.1") {
			t.Fatalf("%s is part of the R6.1 evidence set", id)
		}
	}
	for _, id := range []string{"preflight-reparse", "preflight-unmanaged-service"} {
		if !hasGate(got[id], "R6.3") {
			t.Fatalf("%s is the deferred native preflight", id)
		}
	}
	if !got["downgrade"].Requires.OlderMSI || !got["n1-upgrade"].Requires.OlderMSI || !got["rollback-upgrade"].Requires.OlderMSI {
		t.Fatal("upgrade and downgrade cases require an operator-supplied older MSI")
	}
	if !got["gui-install"].Requires.GUISku || got["system-install"].Requires.Identity != "system" {
		t.Fatal("GUI and SYSTEM cases lost their identity requirements")
	}
	if !got["offline-install"].Requires.Offline {
		t.Fatal("offline install must require a guest with no default route")
	}
	if got["non-admin"].Requires.Identity != "not-elevated" {
		t.Fatal("non-admin case must run without an elevated token")
	}
	nonAdminExits := map[int]bool{}
	for _, code := range got["non-admin"].ExpectExit {
		nonAdminExits[code] = true
	}
	for _, code := range []int{1601, 1602, 1603, 1625} {
		if !nonAdminExits[code] {
			t.Fatalf("non-admin must accept exit %d", code)
		}
	}
	resultFn := extractFunction(t, script, "Invoke-NonAdminResult($Run, [string]$Before) {")
	if !strings.Contains(resultFn, "1601, 1602, 1603, 1625") {
		t.Fatal("non-admin result must allow exit 1601")
	}
	for _, sku := range matrix.ClaimedSKUs {
		if !strings.Contains(evidence, sku) || !strings.Contains(milestones, sku) {
			t.Fatalf("claimed SKU %q is missing from the acceptance record", sku)
		}
	}
	for _, field := range matrix.SummaryFields {
		if !strings.Contains(script, field) {
			t.Fatalf("summary field %s is missing from the harness", field)
		}
	}
	for _, id := range []string{version.UpgradeCode, version.ProductCode, version.BetaUpgradeCode, version.InstallerVersion} {
		if !strings.Contains(script, id) {
			t.Fatalf("harness is missing recorded identity %s", id)
		}
	}
	if strings.Contains(wxs, "ForceReboot") || strings.Contains(wxs, "ScheduleReboot") {
		t.Fatal("package authors a reboot")
	}
	if !strings.Contains(script, "/norestart") {
		t.Fatal("harness dropped /norestart")
	}
	if strings.Contains(script, "winunitd install") || strings.Contains(script, "winunitd uninstall") {
		t.Fatal("harness calls the daemon install verb")
	}
	if !strings.Contains(script, "Evidence directory must stay outside the repository") {
		t.Fatal("harness can write evidence into the repository")
	}
	if !strings.Contains(script, "No recorded N-1 package") && !strings.Contains(script, "no recorded N-1 package") {
		t.Fatal("missing older package must stay an explicit skip")
	}
}

func TestQuietInstallSpecIsOneElevatedCase(t *testing.T) {
	root := moduleRoot(t)
	script := readRepo(t, root, "tests/installer/acceptance.ps1")
	if strings.Contains(script, "return ,") {
		t.Fatal("a returned collection is still wrapped with the unary comma")
	}
	fn := extractFunction(t, script, "ConvertTo-Array($Value) {")
	if !strings.Contains(fn, "return @($Value)") || !strings.Contains(fn, "return @()") {
		t.Fatal("ConvertTo-Array must return a flat list")
	}
	pathFn := extractFunction(t, script, "Get-PathSegments {")
	if !strings.Contains(pathFn, "return $items.ToArray()") {
		t.Fatal("Get-PathSegments must return a flat string array")
	}
	markerFn := extractFunction(t, script, "Get-LogMarkers([string]$Log) {")
	if !strings.Contains(markerFn, "return $found.ToArray()") {
		t.Fatal("Get-LogMarkers must return a flat string array")
	}
	probePath := strings.Replace(pathFn,
		"[Environment]::GetEnvironmentVariable('Path', 'Machine')",
		"$script:PathFixture", 1)
	if probePath == pathFn {
		t.Fatal("Get-PathSegments path source was not isolated")
	}

	exe := powershellExe(t)
	probe := filepath.Join(t.TempDir(), "select-case.ps1")
	body := "param([Parameter(Mandatory)][string]$MatrixPath)\n" +
		fn + extractFunction(t, script, "ConvertTo-PathKey([string]$Path) {") +
		probePath + markerFn + `
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0
$matrix = Get-Content -LiteralPath $MatrixPath -Raw -Encoding utf8 | ConvertFrom-Json
$Case = 'quiet-install'
$spec = $null
$seen = 0
foreach ($item in @(ConvertTo-Array $matrix.cases)) {
	$seen++
	if ($item.id -eq $Case) {
		if ($null -ne $spec) { throw 'quiet-install matched more than once' }
		$spec = $item
	}
}
if ($seen -lt 2) { throw "case list collapsed to $seen" }
if ($null -eq $spec) { throw 'quiet-install was not selected' }
if ($spec -is [System.Array]) { throw 'selected spec is an array' }
$identity = $spec.requires.identity
if ($identity -isnot [string]) { throw 'requires.identity is not a string' }
if ($identity -ne 'elevated') { throw "requires.identity is $identity" }
$one = @(ConvertTo-Array 'elevated')
if ($one.Count -ne 1 -or $one[0] -isnot [string] -or $one[0] -ne 'elevated') { throw 'a single object was not one element' }
$none = @(ConvertTo-Array $null)
if ($none.Count -ne 0) { throw 'null was not an empty list' }

function Assert-PathSegments([string]$Raw, [int]$ExpectCount, [string]$MustMatch) {
	$script:PathFixture = $Raw
	$got = @(Get-PathSegments)
	if ($got.Count -ne $ExpectCount) { throw "path count $($got.Count) for [$Raw]" }
	foreach ($segment in $got) {
		if ($segment -isnot [string]) { throw 'path segment is not a string' }
	}
	if (-not $MustMatch) { return }
	$want = ConvertTo-PathKey $MustMatch
	$hit = $false
	foreach ($segment in $got) {
		if ((ConvertTo-PathKey $segment) -eq $want) { $hit = $true }
	}
	if (-not $hit) { throw 'Program Files bin was not enumerated' }
}
Assert-PathSegments '' 0 ''
Assert-PathSegments 'C:\Windows;;C:\Program Files\winunitd\bin;' 2 'C:\Program Files\winunitd\bin'
Assert-PathSegments 'C:\Program Files\winunitd\bin' 1 'C:\Program Files\winunitd\bin'
function Take-Segments([string[]]$Before) {
	if ($Before.Count -ne 2) { throw "bound path count $($Before.Count)" }
	foreach ($segment in $Before) {
		if ($segment -isnot [string]) { throw 'bound path segment is not a string' }
	}
}
$script:PathFixture = 'C:\Windows;C:\Program Files\winunitd\bin'
Take-Segments @(Get-PathSegments)

$missing = @(Get-LogMarkers '')
if ($missing.Count -ne 0) { throw 'missing log was not an empty marker list' }
$logDir = Join-Path ([IO.Path]::GetTempPath()) ('winunitd-markers-' + [guid]::NewGuid().ToString('n'))
New-Item -ItemType Directory -Path $logDir | Out-Null
try {
	$log = Join-Path $logDir 'sample.log'
	Set-Content -LiteralPath $log -Value ('preflight conflict: reparse point' + [Environment]::NewLine) -Encoding ascii
	$two = @(Get-LogMarkers $log)
	if ($two.Count -ne 2) { throw "markers collapsed to $($two.Count)" }
	foreach ($marker in $two) {
		if ($marker -isnot [string]) { throw 'marker is not a string' }
	}
	if ($two -notcontains 'preflight conflict:' -or $two -notcontains 'reparse point') {
		throw 'known markers were not enumerated'
	}
	Set-Content -LiteralPath $log -Value 'InjectServiceFailure' -Encoding ascii
	$single = @(Get-LogMarkers $log)
	if ($single.Count -ne 1 -or $single[0] -isnot [string] -or $single[0] -ne 'InjectServiceFailure') {
		throw 'a single marker was wrapped'
	}
} finally {
	Remove-Item -LiteralPath $logDir -Recurse -Force
}
`
	if err := os.WriteFile(probe, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-NoProfile", "-NonInteractive", "-File", probe, filepath.Join(root, "tests/installer/acceptance-matrix.json"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("collection enumeration: %v\n%s", err, out)
	}
}

func TestLockedFileHashesAfterRelease(t *testing.T) {
	root := moduleRoot(t)
	script := readRepo(t, root, "tests/installer/acceptance.ps1")
	fn := extractFunction(t, script, "Invoke-LockedFile {")
	lock := strings.Index(fn, "[IO.FileShare]::None")
	msi := strings.Index(fn, "Invoke-Msiexec")
	finally := strings.Index(fn, "\t} finally {")
	if lock < 0 || msi < lock || finally < msi {
		t.Fatal("payload lock must be held through msiexec")
	}
	post := fn[msi:finally]
	service := strings.Index(post, "service state changed")
	release := strings.Index(post, "$stream.Dispose()")
	hash := strings.Index(post, "Get-FileHash")
	if service < 0 || release < service || hash < release {
		t.Fatal("locked-file hash comparison must run after the lock is released")
	}
	if strings.Count(fn, "$stream.Dispose()") < 2 {
		t.Fatal("locked-file must still dispose the lock from finally")
	}
}

func powershellExe(t *testing.T) string {
	t.Helper()
	exe, err := exec.LookPath("pwsh")
	if err != nil {
		exe, err = exec.LookPath("powershell")
	}
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Fatal("powershell is required to check case selection")
		}
		t.Skip("powershell is not installed")
	}
	return exe
}

func extractFunction(t *testing.T, script, signature string) string {
	t.Helper()
	start := "function " + signature
	i := strings.Index(script, start)
	if i < 0 {
		t.Fatalf("%s is missing", signature)
	}
	rest := script[i:]
	j := strings.Index(rest, "\nfunction ")
	if j < 0 {
		t.Fatalf("%s has no following function", signature)
	}
	return rest[:j+1]
}

func TestAcceptanceAlphaGates(t *testing.T) {
	root := moduleRoot(t)
	evidence := readRepo(t, root, "docs/R6-EVIDENCE.md")
	milestones := readRepo(t, root, "docs/MILESTONES.md")
	const evidenceID = "9961798-r64-accept"
	if !strings.Contains(evidence, evidenceID) || !strings.Contains(milestones, evidenceID) {
		t.Fatal("acceptance evidence id 9961798-r64-accept is missing")
	}
	if strings.Contains(evidence, "No acceptance evidence id is recorded.") || strings.Contains(milestones, "No acceptance evidence id is recorded.") {
		t.Fatal("the unrecorded acceptance marker is stale")
	}
	if !strings.Contains(evidence, "2f57d8388d3a5af79ac87409d950cc326af7bf74c91b0f1d54ca33f6cbf0dd23") {
		t.Fatal("acceptance record lost the equal-tree MSI sha256")
	}
	if !strings.Contains(evidence, "56f38c4ffa7aa8d2a953e932603a312ec81eda00ccf6a069fc9c71d6f1d4ef00") {
		t.Fatal("finish record lost the equal-tree MSI sha256")
	}
	if !strings.Contains(evidence, "26100.9168") || !strings.Contains(evidence, "EnterpriseSEval") {
		t.Fatal("acceptance record lost the guest edition or build")
	}
	const notRun = "`gui-install`, `offline-install`, `non-admin`, `beta-conflict`, `downgrade`, `n1-upgrade`, and `rollback-upgrade`"
	if !strings.Contains(evidence, notRun) || !strings.Contains(milestones, "`gui-install`, `offline-install`, `non-admin`, `beta-conflict`, `downgrade`, `n1-upgrade`, and `rollback-upgrade`") {
		t.Fatal("9961798-r64-accept not_run cases must stay listed")
	}
	if !strings.Contains(evidence, "7d21de2-r64-finish") || !strings.Contains(milestones, "7d21de2-r64-finish") {
		t.Fatal("finish evidence id is missing")
	}
	if !strings.Contains(evidence, "f1e38a0-r64-continue") || !strings.Contains(milestones, "f1e38a0-r64-continue") {
		t.Fatal("continuation evidence id was not preserved")
	}
	for _, deferred := range []string{"deferred-media", "deferred-older-msi"} {
		if !strings.Contains(evidence, deferred) || !strings.Contains(milestones, deferred) {
			t.Fatalf("%s must stay listed", deferred)
		}
	}
	if !taskChecked(milestones, "R6.1") || !taskChecked(milestones, "R6.4") {
		t.Fatal("0.1-alpha checks R6.1 and R6.4 from the runnable set")
	}
	if taskChecked(milestones, "R6.5") {
		t.Fatal("R6.5 stays open")
	}
	if !strings.Contains(milestones, "R6.5 and overall R6 stay open") {
		t.Fatal("overall R6 status lost the open R6.5 gate")
	}
	if strings.Contains(milestones, "R6.1, R6.4, and R6.5 stay open") {
		t.Fatal("R6 status still says R6.1 and R6.4 are open")
	}
	if !strings.Contains(milestones, "A3 / R4.4 stay deferred") {
		t.Fatal("A3 / R4.4 deferral is missing from the R6 status")
	}
}

func TestContinuationHarnessUnblocksRemainingCases(t *testing.T) {
	root := moduleRoot(t)
	script := readRepo(t, root, "tests/installer/acceptance.ps1")
	evidence := readRepo(t, root, "docs/R6-EVIDENCE.md")
	build := readRepo(t, root, "packaging/wix/build.ps1")
	if !strings.Contains(evidence, "## Acceptance continuation") || !strings.Contains(evidence, "f1e38a0-r64-continue") {
		t.Fatal("continuation evidence section is missing")
	}
	if !strings.Contains(evidence, "Could not start msiexec as fixture user alice") || !strings.Contains(evidence, "Default route indeterminate") {
		t.Fatal("f1e38a0-r64-continue history was rewritten")
	}
	if !strings.Contains(evidence, "A512B91F-1883-40FD-8EDB-5B8C5708DEEA") {
		t.Fatal("continuation record lost the product UpgradeCode")
	}
	if !strings.Contains(evidence, "| `quiet-install` | 0 |") || !strings.Contains(evidence, "9961798-r64-accept") {
		t.Fatal("9961798-r64-accept history was overwritten")
	}
	if strings.Contains(build, "0.0.1") || !strings.Contains(build, "Record a new ProductCode before changing the installer version") {
		t.Fatal("product build must keep refusing an unrecorded installer version")
	}
	if strings.Contains(script, "$InstallerVersion = '0.0") {
		t.Fatal("harness invented an installer version")
	}
	gui := extractFunction(t, script, "Invoke-CleanInstall([string]$LogName, [bool]$Quiet, [int]$TimeoutSec, [bool]$BasicUi = $false) {")
	if !strings.Contains(gui, "-BasicUi:$BasicUi") {
		t.Fatal("clean install lost the basic UI switch")
	}
	if !strings.Contains(script, "/qb!") || !strings.Contains(script, "UILevel = 3") {
		t.Fatal("gui-install must request basic installer UI")
	}
	interactive := extractFunction(t, script, "Test-InteractiveDesktop {")
	if !strings.Contains(interactive, "SessionId") || !strings.Contains(interactive, "UserInteractive") {
		t.Fatal("interactive detection must require a desktop session")
	}
	if !strings.Contains(interactive, "return $session -gt 0") || strings.Contains(interactive, "return $session -ge 0") {
		t.Fatal("gui-install must keep the session id greater than 0 gate")
	}
	offlineProbe := extractFunction(t, script, "Test-OfflineGuest {")
	load := strings.Index(offlineProbe, "Import-Module NetTCPIP -ErrorAction Stop")
	nullRet := strings.Index(offlineProbe, "return $null")
	v4 := strings.Index(offlineProbe, "Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue")
	v6 := strings.Index(offlineProbe, "Get-NetRoute -DestinationPrefix '::/0' -ErrorAction SilentlyContinue")
	if load < 0 || nullRet < load || v4 < nullRet || v6 < nullRet || !strings.Contains(offlineProbe, "-eq 0") {
		t.Fatal("offline probe must treat an empty route table as offline and stay indeterminate only when NetTCPIP cannot load")
	}
	offline := extractFunction(t, script, "Invoke-OfflineInstall {")
	remove := strings.Index(offline, "Remove-NetRoute")
	restore := strings.Index(offline, "Restore-DefaultRoutes")
	install := strings.Index(offline, "Invoke-CleanInstall 'offline-install'")
	if remove < 0 || install < remove || restore < install {
		t.Fatal("offline-install must remove the default route and restore it after msiexec")
	}
	skip := extractFunction(t, script, "Get-SkipReason($Spec) {")
	if strings.Contains(skip, "default route present") {
		t.Fatal("offline-install still skips when a default route is present")
	}
	if !strings.Contains(skip, "session id greater than 0") {
		t.Fatal("gui-install skip note must name the session id gate")
	}
	nonAdmin := extractFunction(t, script, "Invoke-NonAdmin {")
	if !strings.Contains(nonAdmin, "New-LocalUser -Name 'alice'") || !strings.Contains(nonAdmin, "Remove-LocalUser -Name 'alice'") {
		t.Fatal("non-admin elevated parent must use fixture user alice")
	}
	if !strings.Contains(nonAdmin, "-RunAs 'alice'") {
		t.Fatal("non-admin msiexec must run as alice")
	}
	if !strings.Contains(nonAdmin, "non-admin.err") || !strings.Contains(nonAdmin, "could not start msiexec as fixture user") {
		t.Fatal("non-admin must keep the private error file and the start-failure note")
	}
	if !strings.Contains(script, "LogonUser") || !strings.Contains(script, "CreateProcessAsUser") || !strings.Contains(script, "seclogon") {
		t.Fatal("non-admin start must use LogonUser and CreateProcessAsUser after starting secondary logon")
	}
	if !strings.Contains(script, "Win32 ") {
		t.Fatal("non-admin start failure must surface the Win32 code")
	}
	stopHelper := extractFunction(t, script, "Stop-AcceptanceService {")
	if !strings.Contains(stopHelper, "Stop-Service -Name winunitd -Force") || !strings.Contains(stopHelper, "Wait-ServiceState 'stopped'") {
		t.Fatal("beta cleanup must force-stop winunitd and wait until it is stopped")
	}
	beta := extractFunction(t, script, "Invoke-BetaConflict {")
	stop := strings.Index(beta, "Stop-AcceptanceService")
	betaRemove := strings.Index(beta, "'/x'")
	again := strings.LastIndex(beta, "Stop-AcceptanceService")
	if stop < 0 || betaRemove < 0 || stop > betaRemove || again <= betaRemove || !strings.Contains(beta, "remove exit") {
		t.Fatal("beta cleanup must stop the service before msiexec /x and record the remove exit")
	}
	if !strings.Contains(script, `dist\beta`) || !strings.Contains(script, `packaging\beta`) {
		t.Fatal("beta discovery lost the packaging/beta output layout")
	}
	if !strings.Contains(script, "no recorded N-1 package") {
		t.Fatal("missing older package must stay an explicit skip")
	}
	sku := extractFunction(t, script, "Get-ClaimedSku([string]$Product, [string]$Edition, [string]$Build, [string]$InstallationType) {")
	for _, text := range []string{"Eval", "22000", "20348", "26100", "EnterpriseS", "unclaimed", "Windows Server Core x64"} {
		if !strings.Contains(sku, text) {
			t.Fatalf("claimed SKU classification lost %s", text)
		}
	}
}

func TestAcceptanceHarnessHasNoLabInventory(t *testing.T) {
	root := moduleRoot(t)
	for _, rel := range []string{
		"tests/installer/acceptance.ps1",
		"tests/installer/acceptance-matrix.json",
		"docs/R6-EVIDENCE.md",
		"docs/MILESTONES.md",
	} {
		text := readRepo(t, root, rel)
		if regexp.MustCompile(`S-1-5-21-\d`).MatchString(text) {
			t.Fatalf("%s contains a machine SID", rel)
		}
		lower := strings.ToLower(text)
		if strings.Contains(lower, `c:\users`) || strings.Contains(text, `C:\\Users`) {
			t.Fatalf("%s contains a profile path", rel)
		}
		for _, banned := range []string{"Win32_ComputerSystem", "RegisteredOwner", "Get-ComputerInfo"} {
			if strings.Contains(text, banned) {
				t.Fatalf("%s references %s", rel, banned)
			}
		}
	}
}

func hasGate(item acceptanceCase, gate string) bool {
	for _, candidate := range item.Gate {
		if candidate == gate {
			return true
		}
	}
	return false
}

func taskChecked(markdown, id string) bool {
	return regexp.MustCompile(`(?m)^- \[x\] \*\*` + regexp.QuoteMeta(id) + `\b`).MatchString(markdown)
}

type acceptanceMatrix struct {
	Schema        int              `json:"schema"`
	ClaimedSKUs   []string         `json:"claimed_skus"`
	SummaryFields []string         `json:"summary_fields"`
	Cases         []acceptanceCase `json:"cases"`
}

type acceptanceCase struct {
	ID            string   `json:"id"`
	Gate          []string `json:"gate"`
	Title         string   `json:"title"`
	ExpectExit    []int    `json:"expect_exit"`
	ExpectMarkers []string `json:"expect_markers"`
	Requires      requires `json:"requires"`
}

type requires struct {
	Identity    string `json:"identity"`
	Interactive bool   `json:"interactive"`
	Offline     bool   `json:"offline"`
	Product     string `json:"product"`
	OlderMSI    bool   `json:"older_msi"`
	BetaMSI     bool   `json:"beta_msi"`
	GUISku      bool   `json:"gui_sku"`
}

func decode(t *testing.T, root, rel string, dest any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		t.Fatal(err)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func readRepo(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
