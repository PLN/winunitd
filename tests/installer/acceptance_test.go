package installer_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/version"
)

// requiredCases is the R6.4 acceptance matrix. A skipped case is missing
// evidence. Checking R6.1 or R6.4 in MILESTONES requires replacing the
// unrecorded marker in docs/R6-EVIDENCE.md with a real evidence id from a
// disposable guest, and updating this test to match that record.
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

func TestAcceptanceEvidenceStaysUnchecked(t *testing.T) {
	root := moduleRoot(t)
	evidence := readRepo(t, root, "docs/R6-EVIDENCE.md")
	milestones := readRepo(t, root, "docs/MILESTONES.md")
	if !strings.Contains(evidence, "No acceptance evidence id is recorded.") {
		t.Fatal("acceptance evidence must stay explicitly unrecorded until a lab id replaces this marker")
	}
	if taskChecked(milestones, "R6.1") || taskChecked(milestones, "R6.4") {
		t.Fatal("R6.1 and R6.4 stay unchecked while acceptance evidence is unrecorded")
	}
	if taskChecked(milestones, "R6.5") {
		t.Fatal("R6.5 stays open")
	}
	if !strings.Contains(milestones, "R6.1, R6.4, and R6.5 stay open") {
		t.Fatal("overall R6 status lost the open gates")
	}
	if !strings.Contains(milestones, "A3 / R4.4 stay deferred") {
		t.Fatal("A3 / R4.4 deferral is missing from the R6 status")
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
