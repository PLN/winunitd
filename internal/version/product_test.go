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
		if strings.Contains(text, `ProductCode="*"`) || strings.Contains(text, "CustomAction") || strings.Contains(text, "winunitd install") {
			t.Fatal("installer source uses a generated product code, a custom action, or the daemon install verb")
		}
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
