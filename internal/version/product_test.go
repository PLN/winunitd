package version

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestProductIdentityMatchesInstallerSource(t *testing.T) {
	root := moduleRoot(t)
	wxs := readRepo(t, root, "packaging/wix/Package.wxs")
	proj := readRepo(t, root, "packaging/wix/Winunitd.wixproj")
	script := readRepo(t, root, "packaging/wix/build.ps1")
	for _, text := range []string{wxs, proj, script} {
		if strings.Contains(text, `ProductCode="*"`) || strings.Contains(text, "winunitd install") {
			t.Fatal("installer source uses a generated product code or the daemon install verb")
		}
	}
	if strings.Contains(proj, "CustomAction") {
		t.Fatal("project file must not author custom actions")
	}
	for _, id := range []string{UpgradeCode, ProductCode, BetaUpgradeCode, InstallerVersion} {
		if !strings.Contains(wxs, id) && !strings.Contains(proj, id) && !strings.Contains(script, id) {
			t.Fatalf("recorded identity %s missing from product installer source", id)
		}
	}
	if !strings.Contains(wxs, UpgradeCode) || !strings.Contains(wxs, ProductCode) || !strings.Contains(wxs, BetaUpgradeCode) {
		t.Fatal("package is missing a recorded upgrade, product, or beta identity")
	}
	if !strings.Contains(proj, InstallerVersion) || !strings.Contains(script, InstallerVersion) || !strings.Contains(script, ProductCode) {
		t.Fatal("build project does not pin the recorded installer version and product code")
	}
	allowed := map[string]bool{
		UpgradeCode: true, ProductCode: true, BetaUpgradeCode: true,
	}
	for _, component := range Components {
		needle := `Id="` + component.ID + `" Guid="` + component.GUID + `"`
		if !strings.Contains(wxs, needle) {
			t.Fatalf("component %s is not authored with its recorded GUID", component.ID)
		}
		allowed[component.GUID] = true
	}
	for _, guid := range regexp.MustCompile(`Guid="([0-9A-Fa-f-]{36})"`).FindAllStringSubmatch(wxs, -1) {
		if !allowed[strings.ToUpper(guid[1])] && !allowed[guid[1]] {
			t.Fatalf("unrecorded component GUID %s", guid[1])
		}
	}
	if !strings.Contains(wxs, `EventMessageFile="[#WinunitdExeFile]"`) || !strings.Contains(wxs, `Name="winunitd"`) {
		t.Fatal("event source is not bound to the daemon executable")
	}
	if !strings.Contains(wxs, `ResetPeriodInDays="49710"`) {
		t.Fatal("service recovery reset is not the recorded finite period")
	}
	if strings.Contains(wxs, "<Dialog") || strings.Contains(wxs, "WixUI") {
		t.Fatal("quiet install gained a UI reference")
	}
	for _, needle := range []string{
		`Id="NewServiceTransaction"`,
		`Id="PullTestFail"`,
		`Id="ExtractServiceHelper"`,
		`Id="SetPrepareService"`,
		`Id="RollbackService"`,
		`Id="PrepareService"`,
		`Id="CommitService"`,
		`Id="InjectServiceFailure"`,
		`DllEntry="PullTestFail"`,
		`DllEntry="ExtractServiceHelper"`,
		`DllEntry="RunServiceHelper"`,
		`<InstallUISequence>`,
		`Action="PullTestFail" Before="ExecuteAction"`,
		`Id="MSIRESTARTMANAGERCONTROL" Value="Disable"`,
		`Schedule="afterInstallExecute"`,
		`Execute="rollback"`,
		`Execute="deferred" Impersonate="no"`,
		`Before="StopServices"`,
		`<StopServices Condition="NOT UPGRADINGPRODUCTCODE" />`,
		`<DeleteServices Condition="NOT UPGRADINGPRODUCTCODE" />`,
		`service-prepare`,
		`&quot;[Installed]&quot; &quot;[WIX_UPGRADE_DETECTED]&quot; &quot;[REMOVE]&quot;`,
		`service-rollback`,
		`service-commit`,
		`Binary Id="ServiceHelper" SourceFile="$(Payload)\msi-check.exe"`,
		`Binary Id="ServiceToken" SourceFile="$(Payload)\msi-token.dll"`,
		`Id="WINUNITD_TEST_FAIL" Secure="yes"`,
		`Wait="yes"`,
	} {
		if !strings.Contains(wxs, needle) {
			t.Fatalf("package is missing servicing authoring %s", needle)
		}
	}
	if strings.Contains(wxs, `Before="RemoveExistingProducts"`) || strings.Contains(wxs, `Schedule="afterInstallInitialize"`) {
		t.Fatal("servicing actions must not sit between InstallInitialize and RemoveExistingProducts")
	}
	if strings.Contains(wxs, `File Source="$(Payload)\msi-check.exe"`) || strings.Contains(wxs, `File Source="$(Payload)\msi-token.dll"`) {
		t.Fatal("servicing helper must stay embedded, not installed as a file")
	}
	if !strings.Contains(script, "msi-check.exe") || !strings.Contains(script, "msi-token.dll") || !strings.Contains(script, "16.1.0") || !strings.Contains(script, "-lkernel32") {
		t.Fatal("product build does not produce the embedded helper and transaction DLL")
	}
	token := readRepo(t, root, "tools/msi-token/token.c")
	for _, needle := range []string{
		"GetEnvironmentVariableW",
		"do not clear",
		"CLIENTPROCESSID",
		"MsiProcessMessage",
		"CustomActionData",
		"SELECT `Data` FROM `Binary` WHERE `Name`='ServiceHelper'",
		"service-prepare",
	} {
		if !strings.Contains(token, needle) {
			t.Fatalf("token helper missing %s", needle)
		}
	}
	servicing := readRepo(t, root, "tools/msi-check/servicing.go")
	if !strings.Contains(servicing, "msiServiceControlWait = 30 * time.Second") || !strings.Contains(servicing, "serviceStopBudget = runtime.PreshutdownTimeout") {
		t.Fatal("servicing helper does not record the 30-second MSI wait and the longer stop budget")
	}
	if strings.Contains(wxs, "<CreateFolder KeyPath") {
		t.Fatal("WiX 7 does not allow KeyPath on CreateFolder")
	}
	for _, id := range []string{"DataUnits", "DataEnabled", "DataJournal", "DataRuntime", "DataLinger", "DataDaemon"} {
		if !strings.Contains(wxs, `Id="`+id+`" Guid="`) || !strings.Contains(wxs, `Id="`+id+`" Guid="`+guidOf(id)+`" Permanent="yes" KeyPath="yes"`) {
			t.Fatalf("data directory %s is missing a component key path", id)
		}
	}
}

func guidOf(id string) string {
	for _, component := range Components {
		if component.ID == id {
			return component.GUID
		}
	}
	return ""
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
