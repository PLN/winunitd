package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
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
		// MSI writes a date into Installed for repair, reinstall, and uninstall.
		{"00:00:00", "", "", modeRepair},
		{"20260923000000", "", "", modeRepair},
		{"23:59:59", "", "", modeRepair},
		{"00:00:00", "", "ALL", modeUninstall},
		{"20260923000000", "", "all", modeUninstall},
		{"00:00:00", upgrade, "", modeUpgrade},
		{"20260923000000", "", "AddToPath", modeRepair},
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
		{"24:00:00", "", ""},
		{"00:60:00", "", ""},
		{"00:00:60", "", ""},
		{"20261323000000", "", ""},
		{"20260932000000", "", ""},
		{"2026092300000", "", ""},
		{"00000000000000", "", ""},
		{"2026-09-23", "", ""},
		{"", "00:00:00", ""},
		{"", "20260923000000", ""},
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

func TestProtectedDirectoryReparseMarker(t *testing.T) {
	junction := protectedDirectoryShape(true, true)
	if junction == nil || !strings.Contains(junction.Error(), "reparse point") || !strings.Contains(junction.Error(), "refusing to follow") {
		t.Fatalf("reparse: %v", junction)
	}
	if strings.Contains(junction.Error(), "ordinary directories") {
		t.Fatalf("reparse used the file message: %v", junction)
	}
	logged := fmt.Errorf("preflight conflict: unsafe directory data: %w", junction)
	if !strings.Contains(logged.Error(), "preflight conflict:") || !strings.Contains(logged.Error(), "reparse point") {
		t.Fatalf("log: %v", logged)
	}
	file := protectedDirectoryShape(false, false)
	if file == nil || !strings.Contains(file.Error(), "ordinary directories") || strings.Contains(file.Error(), "reparse point") {
		t.Fatalf("file: %v", file)
	}
	if err := protectedDirectoryShape(false, true); err != nil {
		t.Fatalf("directory: %v", err)
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

// taskDefinition is a complete synthetic Task Scheduler definition with one
// Exec action. The description carries a character outside the Basic
// Multilingual Plane so UTF-16 encodings contain a surrogate pair.
func taskDefinition(command, arguments string) string {
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Author>EXAMPLE\alice</Author>
    <Description>Fixture task ` + "\U0001F4E6" + `</Description>
    <URI>\Fixture</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>S-1-5-18</UserId>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + command + `</Command>
      <Arguments>` + arguments + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// encodeUTF16 writes text as UTF-16 in the given byte order, optionally
// with a byte order mark, as Task Scheduler stores registered definitions.
func encodeUTF16(text string, order binary.AppendByteOrder, bom bool) []byte {
	var out []byte
	if bom {
		out = order.AppendUint16(out, 0xfeff)
	}
	for _, u := range utf16.Encode([]rune(text)) {
		out = order.AppendUint16(out, u)
	}
	return out
}

func scanOneTask(t *testing.T, data []byte) (int, error) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Fixture"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return countTaskLaunchers(root)
}

// A registered task definition is UTF-16LE with a byte order mark. The
// launcher check must see the same direct reference in every supported
// encoding, not only in ASCII bytes.
func TestTaskScanDecodesTaskDefinitions(t *testing.T) {
	direct := taskDefinition(`C:\Program Files\WinUnitD\bin\WINUNITD.Exe`, `--base-dir "C:\Data"`)
	safe := taskDefinition(`C:\Tools\winctl.exe`, `--user status`)
	// Documented boundary: the scan matches an explicit winunitd.exe
	// reference in the definition text only. It does not open a script
	// or follow a wrapper, so this task counts zero even if its script
	// starts a manager. Explicit migration owns per-user and indirect
	// launcher discovery; this case does not qualify that behavior.
	wrapper := taskDefinition(`C:\Windows\System32\wscript.exe`, `//B //Nologo "C:\Pilot\run-manager.vbs"`)
	for _, tt := range []struct {
		name string
		data []byte
		want int
	}{
		{"utf-8 direct mixed case", []byte(direct), 1},
		{"utf-8 with bom direct", append([]byte{0xef, 0xbb, 0xbf}, direct...), 1},
		{"utf-16le with bom direct", encodeUTF16(direct, binary.LittleEndian, true), 1},
		{"utf-16be with bom direct", encodeUTF16(direct, binary.BigEndian, true), 1},
		{"utf-16le without bom direct", encodeUTF16(direct, binary.LittleEndian, false), 1},
		{"utf-16be without bom direct", encodeUTF16(direct, binary.BigEndian, false), 1},
		{"utf-16le with bom safe", encodeUTF16(safe, binary.LittleEndian, true), 0},
		{"utf-8 safe", []byte(safe), 0},
		{"utf-16le with bom wrapper only", encodeUTF16(wrapper, binary.LittleEndian, true), 0},
		{"empty definition", nil, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n, err := scanOneTask(t, tt.data)
			if err != nil || n != tt.want {
				t.Fatalf("launchers=%d err=%v, want %d", n, err, tt.want)
			}
		})
	}
}

// Malformed or unsupported encodings must refuse, never report zero
// launchers, and never be matched after dropping NUL bytes.
func TestTaskScanRefusesMalformedEncodings(t *testing.T) {
	direct := taskDefinition(`C:\Tools\winunitd.exe`, ``)
	le := encodeUTF16(direct, binary.LittleEndian, true)
	unpairedHigh := binary.LittleEndian.AppendUint16(append([]byte{}, le...), 0xd83d)
	unpairedHighMid := append(binary.LittleEndian.AppendUint16([]byte{0xff, 0xfe}, 0xd83d), encodeUTF16("<x/>", binary.LittleEndian, false)...)
	loneLow := append(binary.LittleEndian.AppendUint16([]byte{0xff, 0xfe}, 0xdc00), encodeUTF16("<x/>", binary.LittleEndian, false)...)
	withNUL := binary.LittleEndian.AppendUint16(append([]byte{}, le...), 0)
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"utf-16le odd length", le[:len(le)-1]},
		{"utf-16be odd length", func() []byte { b := encodeUTF16(direct, binary.BigEndian, true); return b[:len(b)-1] }()},
		{"utf-16le truncated surrogate at end", unpairedHigh},
		{"utf-16le unpaired high surrogate", unpairedHighMid},
		{"utf-16le lone low surrogate", loneLow},
		{"utf-16le nul character", withNUL},
		{"invalid utf-8", []byte("<Task><Command>C:\\Tools\\winunitd.exe\xc3\x28</Command></Task>")},
		{"utf-8 with nul byte", []byte("<Task><Command>C:\\Tools\\winunitd.exe\x00</Command></Task>")},
		{"non-ascii first byte then nul", []byte{0xc3, 0x00, '<', 0x00}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n, err := scanOneTask(t, tt.data)
			if err == nil || err.Error() != "preflight conflict: could not inspect machine launchers" || n != 0 {
				t.Fatalf("launchers=%d err=%v, want refusal", n, err)
			}
		})
	}
}

// A mixed store counts each direct definition once, across encodings and
// nested task folders, and ignores safe and wrapper-only definitions.
func TestTaskScanMixedStore(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "Vendor", "Sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		filepath.Join(root, "DirectUTF16"):    encodeUTF16(taskDefinition(`C:\Tools\winunitd.exe`, ``), binary.LittleEndian, true),
		filepath.Join(nested, "DirectUTF8"):   []byte(taskDefinition(`C:\Tools\WinUnitd.EXE`, ``)),
		filepath.Join(root, "Safe"):           encodeUTF16(taskDefinition(`C:\Tools\winctl.exe`, ``), binary.LittleEndian, true),
		filepath.Join(root, "Vendor", "Wrap"): encodeUTF16(taskDefinition(`wscript.exe`, `run.vbs`), binary.LittleEndian, true),
	}
	for path, data := range files {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n, err := countTaskLaunchers(root)
	if err != nil || n != 2 {
		t.Fatalf("launchers=%d err=%v, want 2", n, err)
	}
}

// A launcher found in a UTF-16 definition blocks install, repair and
// upgrade with the exact migration conflict; uninstall stays exempt.
func TestDecodedTaskLauncherClassification(t *testing.T) {
	n, err := scanOneTask(t, encodeUTF16(taskDefinition(`C:\Tools\winunitd.exe`, ``), binary.LittleEndian, true))
	if err != nil || n != 1 {
		t.Fatalf("launchers=%d err=%v", n, err)
	}
	install := `C:\Program Files\winunitd`
	data := `C:\ProgramData\winunitd`
	owned := serviceFacts{Exists: true, DecomposeOK: true, Binary: filepath.Join(install, "bin", "winunitd.exe"), BaseDir: data, LocalSystem: true}
	const want = "preflight conflict: incompatible pilot launches winunitd; explicit migration is required"
	for _, tt := range []struct {
		mode    string
		service serviceFacts
	}{
		{modeInstall, serviceFacts{}},
		{modeRepair, owned},
		{modeUpgrade, owned},
	} {
		err := evaluatePreflight(tt.mode, nil, tt.service, install, data, n)
		if err == nil || err.Error() != want {
			t.Fatalf("%s: %v", tt.mode, err)
		}
	}
	if err := evaluatePreflight(modeUninstall, nil, owned, install, data, n); err != nil {
		t.Fatalf("uninstall blocked by pilot: %v", err)
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
