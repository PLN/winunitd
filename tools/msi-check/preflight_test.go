package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightMode(t *testing.T) {
	product := "{78C43374-5AB7-4E81-B9CF-09E8ACD01133}"
	upgrade := "{A512B91F-1883-40FD-8EDB-5B8C5708DEEA}"
	for _, tt := range []struct {
		installed, upgrade, remove, want string
	}{
		{"", "", "", modeInstall},
		{product, "", "", modeRepair},
		{"78C43374-5AB7-4E81-B9CF-09E8ACD01133", "", "", modeRepair},
		{"", upgrade, "", modeUpgrade},
		{product, upgrade, "", modeUpgrade},
		{product, "", "ALL", modeUninstall},
		{"", "", "all", modeUninstall},
		{product, "", "AddToPath", modeRepair},
		{"", "", "Main,AddToPath", modeRepair},
	} {
		got, err := preflightMode(tt.installed, tt.upgrade, tt.remove)
		if err != nil || got != tt.want {
			t.Fatalf("installed=%q upgrade=%q remove=%q: mode=%s err=%v", tt.installed, tt.upgrade, tt.remove, got, err)
		}
	}
	for _, bad := range [][3]string{
		{"not-a-guid", "", ""},
		{"", "../state", ""},
		{"", "", `ALL" & calc`},
		{"", "", "..\\Windows"},
	} {
		if _, err := preflightMode(bad[0], bad[1], bad[2]); err == nil || !strings.Contains(err.Error(), "invalid installer context") {
			t.Fatalf("accepted %q: %v", bad, err)
		}
	}
}

func TestClassifyServiceConflicts(t *testing.T) {
	install := `C:\Program Files\winunitd`
	data := `C:\ProgramData\winunitd`
	owned := serviceFacts{
		Exists:      true,
		DecomposeOK: true,
		Binary:      filepath.Join(install, "bin", "winunitd.exe"),
		BaseDir:     data + `\.`,
		LocalSystem: true,
	}
	if err := classifyService(modeRepair, owned, install, data); err != nil {
		t.Fatal(err)
	}
	if err := classifyService(modeUpgrade, owned, install, data); err != nil {
		t.Fatal(err)
	}
	if err := classifyService(modeUninstall, owned, install, data); err != nil {
		t.Fatal(err)
	}
	if err := classifyService(modeInstall, serviceFacts{}, install, data); err != nil {
		t.Fatal(err)
	}
	err := classifyService(modeInstall, owned, install, data)
	if err == nil || !strings.Contains(err.Error(), "not owned by this package") {
		t.Fatalf("fresh install adopted an existing service: %v", err)
	}
	unmanaged := owned
	unmanaged.Binary = filepath.Join(install, "winunitd.exe")
	err = classifyService(modeRepair, unmanaged, install, data)
	if err == nil || !strings.Contains(err.Error(), "unmanaged winunitd binary path") {
		t.Fatalf("unmanaged binary: %v", err)
	}
	custom := owned
	custom.BaseDir = `D:\Operator\winunitd`
	err = classifyService(modeUpgrade, custom, install, data)
	if err == nil || !strings.Contains(err.Error(), "custom base directory") || !strings.Contains(err.Error(), "explicit migration is required") {
		t.Fatalf("custom base: %v", err)
	}
	other := owned
	other.LocalSystem = false
	err = classifyService(modeRepair, other, install, data)
	if err == nil || !strings.Contains(err.Error(), "unrelated pre-existing winunitd service") {
		t.Fatalf("other account: %v", err)
	}
	broken := serviceFacts{Exists: true}
	err = classifyService(modeUninstall, broken, install, data)
	if err == nil || !strings.Contains(err.Error(), "unmanaged winunitd binary path") {
		t.Fatalf("uninstall of an unrelated service: %v", err)
	}
}

func TestClassifyDirectoryAndPilot(t *testing.T) {
	safe := dirFact{Name: "units", Directory: true, TrustedOwn: true, Restrictive: true}
	if err := classifyDirectory(safe); err != nil {
		t.Fatal(err)
	}
	if err := classifyDirectory(dirFact{Name: "journal", Missing: true}); err != nil {
		t.Fatal(err)
	}
	err := classifyDirectory(dirFact{Name: "enabled", Reparse: true, Directory: true, TrustedOwn: true, Restrictive: true})
	if err == nil || !strings.Contains(err.Error(), "reparse point") || !strings.Contains(err.Error(), "refusing to follow") {
		t.Fatalf("reparse: %v", err)
	}
	err = classifyDirectory(dirFact{Name: "linger", Directory: true, Restrictive: true})
	if err == nil || !strings.Contains(err.Error(), "unexpected ownership") {
		t.Fatalf("owner: %v", err)
	}
	err = classifyDirectory(dirFact{Name: "runtime", Directory: true, TrustedOwn: true})
	if err == nil || !strings.Contains(err.Error(), "non-administrator writes") {
		t.Fatalf("dacl: %v", err)
	}
	err = classifyDirectory(dirFact{Name: `C:\secret\units`, Directory: false})
	if err == nil || strings.Contains(err.Error(), `C:\secret`) {
		t.Fatalf("path leaked: %v", err)
	}
	install := `C:\Program Files\winunitd`
	data := `C:\ProgramData\winunitd`
	reparse := []dirFact{{Name: "units", Reparse: true}}
	service := serviceFacts{Exists: true, DecomposeOK: true, Binary: filepath.Join(install, "bin", "winunitd.exe"), BaseDir: data, LocalSystem: true}
	err = evaluatePreflight(modeInstall, reparse, service, install, data, 1)
	if err == nil || !strings.Contains(err.Error(), "reparse point") {
		t.Fatalf("directory check did not win: %v", err)
	}
	err = evaluatePreflight(modeInstall, []dirFact{{Name: "units", Missing: true}}, service, install, data, 1)
	if err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("service check did not win: %v", err)
	}
	err = evaluatePreflight(modeRepair, nil, service, install, data, 2)
	if err == nil || !strings.Contains(err.Error(), "incompatible pilot") || !strings.Contains(err.Error(), "explicit migration is required") {
		t.Fatalf("pilot: %v", err)
	}
	if err := evaluatePreflight(modeUninstall, nil, service, install, data, 2); err != nil {
		t.Fatalf("uninstall blocked by pilot: %v", err)
	}
}

func TestTaskScanDoesNotFollowReparse(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "hidden.xml")
	body := []byte(`<Command>C:\Tools\winunitd.exe</Command>`)
	if err := os.WriteFile(target, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "pilot.xml")); err != nil {
		t.Fatal(err)
	}
	_, err := countTaskLaunchers(root)
	if err == nil || !strings.Contains(err.Error(), "reparse point") || !strings.Contains(err.Error(), "refusing to follow") {
		t.Fatalf("followed or ignored reparse: %v", err)
	}
	plain := t.TempDir()
	if err := os.WriteFile(filepath.Join(plain, "alice.xml"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "bob.xml"), []byte(`<Command>C:\Tools\winctl.exe</Command>`), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := countTaskLaunchers(plain)
	if err != nil || n != 1 {
		t.Fatalf("launchers=%d err=%v", n, err)
	}
	if got := countRunLaunchers([]string{`C:\Tools\winctl.exe`, `C:\Tools\winunitd.exe --base-dir C:\Data`}); got != 1 {
		t.Fatalf("run launchers=%d", got)
	}
}

func TestMutableDataOutsideFileComponents(t *testing.T) {
	wxs := readPackageWxs(t)
	section := dataSection(t, wxs)
	if strings.Contains(section, "<File") || strings.Contains(section, "RemoveFile") || strings.Contains(strings.ToLower(section), "purge") {
		t.Fatal("mutable data tree authors a file, removal, or purge")
	}
	for _, id := range []string{"DataUnits", "DataEnabled", "DataJournal", "DataRuntime", "DataLinger", "DataDaemon"} {
		body := componentBody(t, wxs, id)
		if !strings.Contains(body, `Permanent="yes"`) || !strings.Contains(body, "<CreateFolder") {
			t.Fatalf("%s is not a permanent directory component", id)
		}
		if strings.Contains(body, "PermissionEx") || strings.Contains(body, "<File") {
			t.Fatalf("%s would reapply ACLs or replace files on repair", id)
		}
	}
	root := componentBody(t, wxs, "DataRoot")
	if !strings.Contains(root, "PermissionEx") || strings.Contains(root, "<File") {
		t.Fatal("data root lost its anchor ACL or gained a file")
	}
	for _, leak := range []string{"LocalAppData", "USERPROFILE", "AppData\\Local", "S-1-5-21-", "Purge="} {
		if strings.Contains(wxs, leak) {
			t.Fatalf("package authoring contains %s", leak)
		}
	}
	if strings.Count(wxs, "<File ") != len(packagePayloadPaths()) {
		t.Fatalf("file component count = %d", strings.Count(wxs, "<File "))
	}
	for _, id := range []string{"WinunitdExeFile", "WinctlExeFile", "NotifyExeFile", "LicenseFile", "NoticesFile", "InstallDocFile", "UnitReferenceFile", "ExampleServiceFile", "ExampleTargetFile", "ExampleReadmeFile"} {
		if !strings.Contains(wxs, `Id="`+id+`"`) {
			t.Fatalf("missing package file %s", id)
		}
	}
}

func TestRepairUpgradeUninstallRetainMutableFixture(t *testing.T) {
	fixture := map[string]string{
		"data/units/alice-worker.service":                                              "[Service]\nExecStart=C:\\Tools\\alice-worker.exe\n",
		"data/enabled/default.target/alice-worker.service":                             "enabled\n",
		"data/journal/alice-worker.service.log":                                        "{\"v\":4,\"stream\":\"alice\"}\n",
		"data/runtime/timers/alice-worker.service.json":                                "{\"version\":2,\"lastSuccess\":\"2026-01-01T00:00:00Z\"}\n",
		"data/linger/S-1-5-21-1-2-3-1001":                                              "{\"sid\":\"S-1-5-21-1-2-3-1001\",\"name\":\"alice\"}\n",
		"data/daemon/daemon.log":                                                       "alice boot\n",
		"users/alice/AppData/Local/winunitd/units/bob-helper.service":                  "[Service]\nExecStart=C:\\Tools\\bob-helper.exe\n",
		"users/carol/AppData/Local/winunitd/enabled/default.target/bob-helper.service": "enabled\n",
		"users/bob/AppData/Local/winunitd/runtime/timers/carol.timer.json":             "{\"version\":1}\n",
	}
	for _, op := range []string{"repair", "upgrade", "uninstall"} {
		t.Run(op, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, fixture)
			before := hashTree(t, root, fixture)
			if err := applyPackageServicing(root, op, packagePayloadPaths()); err != nil {
				t.Fatal(err)
			}
			after := hashTree(t, root, fixture)
			for rel, sum := range before {
				if after[rel] != sum {
					t.Fatalf("%s clobbered %s", op, rel)
				}
			}
			exe := filepath.Join(root, filepath.FromSlash(packagePayloadPaths()[0]))
			payload, err := os.ReadFile(exe)
			if op == "uninstall" {
				if !os.IsNotExist(err) {
					t.Fatalf("uninstall left package file: %v", err)
				}
			} else if err != nil || string(payload) != "package-bytes-"+op {
				t.Fatalf("repair payload = %q err=%v", payload, err)
			}
			units, err := os.ReadDir(filepath.Join(root, "data", "units"))
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(units))
			for _, unit := range units {
				names = append(names, unit.Name())
			}
			if len(names) != 1 || names[0] != "alice-worker.service" {
				t.Fatalf("units after %s: %v", op, names)
			}
		})
	}
}

func TestServicingDoesNotFollowMutableReparse(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(target, "secret")
	if err := os.WriteFile(secret, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "data", "units")); err != nil {
		t.Fatal(err)
	}
	err := applyPackageServicing(root, "repair", packagePayloadPaths())
	if err == nil || !strings.Contains(err.Error(), "reparse point") || !strings.Contains(err.Error(), "units") {
		t.Fatalf("repair followed units: %v", err)
	}
	got, err := os.ReadFile(secret)
	if err != nil || string(got) != "keep" {
		t.Fatalf("target changed: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "pkg", "winunitd", "bin", "winunitd.exe")); !os.IsNotExist(err) {
		t.Fatal("repair wrote package files after a reparse rejection")
	}
}

func applyPackageServicing(root, op string, packageRel []string) error {
	if err := mutableDirReparse(root); err != nil {
		return err
	}
	switch op {
	case "repair", "upgrade":
		for _, rel := range packageRel {
			path := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte("package-bytes-"+op), 0o644); err != nil {
				return err
			}
		}
	case "uninstall":
		for _, rel := range packageRel {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	default:
		return os.ErrInvalid
	}
	return nil
}

func mutableDirReparse(root string) error {
	for _, name := range mutableDataDirs {
		path := filepath.Join(root, "data", name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		reparse, err := pathIsReparse(path, info)
		if err != nil {
			return err
		}
		if reparse {
			return classifyDirectory(dirFact{Name: name, Reparse: true})
		}
	}
	return nil
}

func packagePayloadPaths() []string {
	return []string{
		"pkg/winunitd/bin/winunitd.exe",
		"pkg/winunitd/bin/winctl.exe",
		"pkg/winunitd/bin/winunit-notify.exe",
		"pkg/winunitd/doc/LICENSE",
		"pkg/winunitd/doc/THIRD-PARTY-NOTICES.txt",
		"pkg/winunitd/doc/INSTALLATION.md",
		"pkg/winunitd/doc/UNIT-REFERENCE.md",
		"pkg/winunitd/doc/examples/worker.service",
		"pkg/winunitd/doc/examples/worker.target",
		"pkg/winunitd/doc/examples/README.md",
	}
}

func writeFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func hashTree(t *testing.T, root string, files map[string]string) map[string][32]byte {
	t.Helper()
	out := make(map[string][32]byte, len(files))
	for rel := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		out[rel] = sha256.Sum256(data)
	}
	return out
}

func readPackageWxs(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "..", "..", "packaging", "wix", "Package.wxs"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func dataSection(t *testing.T, wxs string) string {
	t.Helper()
	start := strings.Index(wxs, `Id="CommonAppDataFolder"`)
	if start < 0 {
		t.Fatal("missing data directory")
	}
	rest := wxs[start:]
	end := strings.Index(rest, "</StandardDirectory>")
	if end < 0 {
		t.Fatal("unclosed data directory")
	}
	return rest[:end]
}

func componentBody(t *testing.T, wxs, id string) string {
	t.Helper()
	needle := `Id="` + id + `"`
	i := strings.Index(wxs, needle)
	if i < 0 {
		t.Fatalf("missing %s", id)
	}
	start := strings.LastIndex(wxs[:i], "<Component")
	endRel := strings.Index(wxs[i:], "</Component>")
	if start < 0 || endRel < 0 {
		t.Fatalf("unclosed %s", id)
	}
	return wxs[start : i+endRel+len("</Component>")]
}
