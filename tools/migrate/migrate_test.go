package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHappyPathDiscoverDryRunApplyHealthRollback(t *testing.T) {
	root := t.TempDir()
	backupDir := t.TempDir()
	msi := writeMSI(t)
	if code := run(args(root, "init", "-scenario", "happy"), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("init")
	}
	before := readFixture(t, root)
	unitBefore := readRel(t, root, "users/alice/pilot/units/fixture.service")

	rep, code := runReport(t, args(root, "discover")...)
	if code != 0 || rep.Blocked {
		t.Fatalf("discover blocked=%v code=%d conflicts=%v", rep.Blocked, code, rep.Conflicts)
	}
	if len(rep.Preflight) != 0 {
		t.Fatalf("machine preflight rejects: %+v", rep.Preflight)
	}
	if !hasCode(rep.UserMoves, "disable-pilot") || !hasCode(rep.UserMoves, "copy-unit") || !hasCode(rep.UserMoves, "archive-journal") {
		t.Fatalf("user moves: %+v", rep.UserMoves)
	}

	rep, code = runReport(t, args(root, "dry-run", "-msi", msi)...)
	if code != 0 || rep.Blocked {
		t.Fatalf("dry-run code=%d blocked=%v conflicts=%v", code, rep.Blocked, rep.Conflicts)
	}
	if !hasOp(rep.Actions, "disable-task") || !hasOp(rep.Actions, "stop-process") || !hasOp(rep.Actions, "copy-unit") || !hasOp(rep.Actions, "install-msi") || !hasOp(rep.Actions, "health-check") {
		t.Fatalf("actions: %+v", rep.Actions)
	}
	if strings.Contains(rep.Actions[0].Detail, msi) || jsonContains(t, rep, msi) {
		t.Fatal("dry-run leaked the MSI path")
	}
	if !bytes.Equal(before, readFixture(t, root)) || !bytes.Equal(unitBefore, readRel(t, root, "users/alice/pilot/units/fixture.service")) {
		t.Fatal("dry-run mutated the fixture")
	}

	if code := run(args(root, "backup", "-out", backupDir), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("backup")
	}
	if _, err := os.Stat(filepath.Join(backupDir, "tasks", "alice", "alice-pilot.xml")); err != nil {
		t.Fatal("task definition was not exported")
	}
	if _, err := os.Stat(filepath.Join(backupDir, "files", "users", "alice", "pilot", "journal", "fixture.log")); err != nil {
		t.Fatal("journal was not archived")
	}

	rep, code = runReport(t, args(root, "apply", "-msi", msi, "-backup", backupDir)...)
	if code != 0 {
		t.Fatalf("apply code=%d", code)
	}
	if !containsAll(rep.Health, "service-identity", "control-owner", "workload-unit", "pilot-disabled") {
		t.Fatalf("health: %+v", rep.Health)
	}
	fx := loadOK(t, root)
	if !serviceManaged(fx.Service) || !fx.Service.Running {
		t.Fatalf("service: %+v", fx.Service)
	}
	alice := userByName(fx, "alice")
	if alice.Tasks[0].Enabled || alice.Tasks[1].Enabled || alice.Processes[0].Running {
		t.Fatal("pilot ownership was not handed off")
	}
	if !bytes.Equal(unitBefore, readRel(t, root, "users/alice/local/winunitd/units/fixture.service")) {
		t.Fatal("unit was not copied")
	}
	if _, err := os.Stat(filepath.Join(root, "users", "alice", "local", "winunitd", "journal", "fixture.log")); !os.IsNotExist(err) {
		t.Fatal("journal was written into the destination")
	}
	if string(readRel(t, root, "users/alice/pilot/journal/fixture.log")) != fixtureJournal {
		t.Fatal("source journal changed")
	}
	if string(readRel(t, root, "users/alice/local/winunitd/runtime/control-owner")) != controlOwner {
		t.Fatal("control owner marker missing")
	}

	if code := run(args(root, "health"), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("health")
	}
	if code := run(args(root, "rollback", "-backup", backupDir), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("rollback")
	}
	if _, err := os.Stat(filepath.Join(backupDir, "manifest.json")); err != nil {
		t.Fatal("backup was not retained")
	}
	restored := loadOK(t, root)
	alice = userByName(restored, "alice")
	if !alice.Tasks[0].Enabled || !alice.Processes[0].Running || restored.Service.Exists {
		t.Fatal("rollback did not restore the pilot")
	}
	if _, err := os.Stat(filepath.Join(root, "users", "alice", "local", "winunitd", "units", "fixture.service")); !os.IsNotExist(err) {
		t.Fatal("rollback left the copied unit")
	}
}

func TestInjectedFailureRestoresPilot(t *testing.T) {
	root, backupDir, msi := happyReady(t)
	journal := filepath.Join(root, "users", "alice", "local", "winunitd", "journal", "fixture.log")
	if err := os.MkdirAll(filepath.Dir(journal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := run(args(root, "apply", "-msi", msi, "-backup", backupDir, "-fail-after", "copy"), new(bytes.Buffer), &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "injected failure after copy") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	fx := loadOK(t, root)
	alice := userByName(fx, "alice")
	if !alice.Tasks[0].Enabled || !alice.Tasks[1].Enabled || !alice.Processes[0].Running || fx.Service.Exists {
		t.Fatalf("inverse state: tasks=%v process=%v service=%+v", alice.Tasks, alice.Processes, fx.Service)
	}
	if _, err := os.Stat(filepath.Join(root, "users", "alice", "local", "winunitd", "units", "fixture.service")); !os.IsNotExist(err) {
		t.Fatal("inverse left the copied unit")
	}
	if string(mustRead(t, journal)) != "keep\n" {
		t.Fatal("inverse overwrote the destination journal")
	}
	if _, err := os.Stat(filepath.Join(backupDir, "fixture.json")); err != nil {
		t.Fatal("backup was not retained")
	}
}

func TestConflictsFailClosed(t *testing.T) {
	msi := writeMSI(t)
	cases := []struct {
		scenario string
		code     string
	}{
		{"custom-base-dir", "custom-base-dir"},
		{"unmanaged-service", "unmanaged-service"},
		{"non-localsystem", "non-localsystem"},
		{"beta", "beta-upgrade-code"},
		{"duplicate-launchers", "duplicate-launchers"},
		{"machine-launcher", "machine-launcher"},
		{"machine-run", "machine-launcher"},
		{"reparse", "reparse"},
		{"unsafe-data", "unsafe-data-dir"},
		{"collision", "destination-collision"},
	}
	for _, tc := range cases {
		t.Run(tc.scenario, func(t *testing.T) {
			root := t.TempDir()
			backupDir := t.TempDir()
			if code := run(args(root, "init", "-scenario", tc.scenario), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
				t.Fatal("init")
			}
			before := readFixture(t, root)
			rep, code := runReport(t, args(root, "dry-run", "-msi", msi)...)
			if code != 2 || !rep.Blocked || !hasCode(rep.Conflicts, tc.code) {
				t.Fatalf("code=%d blocked=%v conflicts=%v", code, rep.Blocked, rep.Conflicts)
			}
			if code := run(args(root, "backup", "-out", backupDir), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
				t.Fatal("backup")
			}
			var stderr bytes.Buffer
			if code := run(args(root, "apply", "-msi", msi, "-backup", backupDir), new(bytes.Buffer), &stderr); code != 2 {
				t.Fatalf("apply code=%d stderr=%s", code, stderr.String())
			}
			if !bytes.Equal(before, readFixture(t, root)) {
				t.Fatal("conflict apply mutated the fixture")
			}
		})
	}
}

func TestUserContextRequired(t *testing.T) {
	root := t.TempDir()
	if code := run(args(root, "init", "-scenario", "happy"), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal(code)
	}
	rep, code := runReport(t, args(root, "-user=false", "discover")...)
	if code != 2 || !hasCode(rep.Conflicts, "user-context-required") || len(rep.Preflight) != 0 {
		t.Fatalf("code=%d preflight=%v conflicts=%v", code, rep.Preflight, rep.Conflicts)
	}
	if hasCode(rep.UserMoves, "copy-unit") {
		t.Fatal("machine-only discovery claimed a user copy")
	}
}

func TestLingerStaysUnlessOptIn(t *testing.T) {
	msi := writeMSI(t)
	root, backupDir := initBackup(t, "linger")
	rep, code := runReport(t, args(root, "dry-run", "-msi", msi)...)
	if code != 0 || !hasOp(rep.Actions, "leave-linger") {
		t.Fatalf("code=%d actions=%v", code, rep.Actions)
	}
	if code := run(args(root, "apply", "-msi", msi, "-backup", backupDir), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("apply")
	}
	if _, err := os.Stat(filepath.Join(root, "users", "alice", "local", "winunitd", "linger", "record")); !os.IsNotExist(err) {
		t.Fatal("linger was copied without opt-in")
	}
	if string(readRel(t, root, "users/alice/pilot/linger/record")) != "opt-in\n" {
		t.Fatal("pilot linger record changed")
	}

	root, backupDir = initBackup(t, "linger")
	if code := run(args(root, "-linger", "apply", "-msi", msi, "-backup", backupDir), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("opt-in apply")
	}
	if string(readRel(t, root, "users/alice/local/winunitd/linger/record")) != "opt-in\n" {
		t.Fatal("explicit linger opt-in did not copy")
	}
}

func TestChangedFixtureRefusesApply(t *testing.T) {
	root, backupDir, msi := happyReady(t)
	fx := loadOK(t, root)
	fx.Users[0].Tasks[0].Enabled = false
	if err := saveFixture(root, fx); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := run(args(root, "apply", "-msi", msi, "-backup", backupDir), new(bytes.Buffer), &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "fixture changed after backup") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "users", "alice", "local", "winunitd", "units", "fixture.service")); !os.IsNotExist(err) {
		t.Fatal("refused apply still copied")
	}
}

func TestMissingMSIBlocksBeforeCopy(t *testing.T) {
	root, backupDir := initBackup(t, "happy")
	rep, code := runReport(t, args(root, "dry-run")...)
	if code != 2 || !hasCode(rep.Conflicts, "msi-required") {
		t.Fatalf("code=%d conflicts=%v", code, rep.Conflicts)
	}
	var stderr bytes.Buffer
	if code := run(args(root, "apply", "-backup", backupDir), new(bytes.Buffer), &stderr); code != 2 {
		t.Fatalf("apply code=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "users", "alice", "local", "winunitd", "units", "fixture.service")); !os.IsNotExist(err) {
		t.Fatal("missing MSI still copied")
	}
}

func TestExecuteMSIRequiresWindowsBeforeMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("msiexec execution is native-only and is not part of this fixture test")
	}
	root, backupDir, msi := happyReady(t)
	before := readFixture(t, root)
	var stderr bytes.Buffer
	code := run(args(root, "-execute-msi", "apply", "-msi", msi, "-backup", backupDir), new(bytes.Buffer), &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "msiexec requires Windows") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !bytes.Equal(before, readFixture(t, root)) {
		t.Fatal("unsupported msiexec mutated the fixture")
	}
}

func TestExistingProductServiceDoesNotReinstall(t *testing.T) {
	root := t.TempDir()
	backupDir := t.TempDir()
	if code := run(args(root, "init", "-scenario", "happy"), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("init")
	}
	fx := loadOK(t, root)
	fx.Service = serviceState{Exists: true, Binary: productBinary, BaseDir: standardBase, LocalSystem: true, Running: false}
	if err := saveFixture(root, fx); err != nil {
		t.Fatal(err)
	}
	if code := run(args(root, "backup", "-out", backupDir), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("backup")
	}
	rep, code := runReport(t, args(root, "dry-run")...)
	if code != 0 || hasOp(rep.Actions, "install-msi") || !hasOp(rep.Actions, "start-service") {
		t.Fatalf("code=%d actions=%v conflicts=%v", code, rep.Actions, rep.Conflicts)
	}
	if code := run(args(root, "apply", "-backup", backupDir), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("apply")
	}
	got := loadOK(t, root)
	if !serviceManaged(got.Service) || !got.Service.Running {
		t.Fatalf("service: %+v", got.Service)
	}
}

func TestUnchangedDestinationSurvivesRollback(t *testing.T) {
	root, backupDir, msi := happyReady(t)
	dest := filepath.Join(root, "users", "alice", "local", "winunitd", "units", "fixture.service")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte(fixtureUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	// The pre-existing file has to be in the destination snapshot apply records.
	if code := run(args(root, "apply", "-msi", msi, "-backup", backupDir), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("apply")
	}
	if code := run(args(root, "rollback", "-backup", backupDir), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("rollback")
	}
	if string(mustRead(t, dest)) != fixtureUnit {
		t.Fatal("rollback removed an unchanged destination file")
	}
}

func TestSymlinkPilotIsReparse(t *testing.T) {
	root, _, msi := happyReady(t)
	units := filepath.Join(root, "users", "alice", "pilot", "units")
	alt := filepath.Join(root, "alt-units")
	if err := os.MkdirAll(alt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(units, filepath.Join(alt, "units")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(alt, "units"), units); err != nil {
		t.Skip("symlink is not available")
	}
	rep, code := runReport(t, args(root, "dry-run", "-msi", msi)...)
	if code != 2 || !hasCode(rep.Conflicts, "reparse") {
		t.Fatalf("code=%d conflicts=%v", code, rep.Conflicts)
	}
}

func TestFixtureAccountAndPrivateMarkerRejected(t *testing.T) {
	root := t.TempDir()
	body := []byte(`{"service":{"exists":false,"local_system":false,"running":false},"data_reparse":false,"data_unsafe":false,"machine_tasks":[],"machine_run":[],"users":[{"name":"dave","tasks":[],"processes":[],"linger_opt_in":false,"pilot_rel":"users/dave/pilot","dest_rel":"users/dave/local/winunitd"}]}`)
	if err := os.WriteFile(filepath.Join(root, fixtureFile), append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := run(args(root, "discover"), new(bytes.Buffer), &stderr); code != 1 || !strings.Contains(stderr.String(), "alice, bob, or carol") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	marked := bytes.ReplaceAll(body, []byte(`"dave"`), []byte(`"alice"`))
	marked = bytes.ReplaceAll(marked, []byte(`"tasks":[]`), []byte(`"tasks":[{"name":"alice-pilot","command":"S-1-5-x","enabled":true,"manager":false,"definition":"task"}]`))
	if err := os.WriteFile(filepath.Join(root, fixtureFile), marked, 0o644); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run(args(root, "discover"), new(bytes.Buffer), &stderr); code != 1 || !strings.Contains(stderr.String(), "private account marker") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestBackupMustStayOutsideFixture(t *testing.T) {
	root := t.TempDir()
	if code := run(args(root, "init", "-scenario", "happy"), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("init")
	}
	var stderr bytes.Buffer
	code := run(args(root, "backup", "-out", filepath.Join(root, "backup")), new(bytes.Buffer), &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "outside the fixture") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestFixtureScriptHasNoPrivateMarkers(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("fixture", "Invoke-MigrateFixture.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, banned := range []string{"S-1-5-", "HermesHome", "192.168.", "password", "C:\\Users\\"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(banned)) {
			t.Fatalf("fixture script contains %s", banned)
		}
	}
	for _, need := range []string{"discover", "backup", "dry-run", "apply", "health", "-fail-after", "copy", "custom-base-dir", "alice"} {
		if !strings.Contains(text, need) {
			t.Fatalf("fixture script missing %s", need)
		}
	}
}

func happyReady(t *testing.T) (root, backupDir, msi string) {
	t.Helper()
	root, backupDir = initBackup(t, "happy")
	return root, backupDir, writeMSI(t)
}

func initBackup(t *testing.T, scenario string) (string, string) {
	t.Helper()
	root := t.TempDir()
	backupDir := t.TempDir()
	if code := run(args(root, "init", "-scenario", scenario), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("init")
	}
	if code := run(args(root, "backup", "-out", backupDir), new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal("backup")
	}
	return root, backupDir
}

func writeMSI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caller-supplied.msi")
	if err := os.WriteFile(path, []byte{1, 2, 3, 4}, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func args(root string, extra ...string) []string {
	out := []string{"-root", root}
	return append(out, extra...)
}

func runReport(t *testing.T, argv ...string) (report, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(argv, &stdout, &stderr)
	if stdout.Len() == 0 {
		t.Fatalf("no report code=%d stderr=%s", code, stderr.String())
	}
	var rep report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("report: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return rep, code
}

func loadOK(t *testing.T, root string) fixture {
	t.Helper()
	fx, err := loadFixture(root)
	if err != nil {
		t.Fatal(err)
	}
	return fx
}

func readFixture(t *testing.T, root string) []byte {
	t.Helper()
	return mustRead(t, filepath.Join(root, fixtureFile))
}

func readRel(t *testing.T, root, rel string) []byte {
	t.Helper()
	return mustRead(t, filepath.Join(root, filepath.FromSlash(rel)))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func userByName(fx fixture, name string) userState {
	for _, user := range fx.Users {
		if user.Name == name {
			return user
		}
	}
	return userState{}
}

func hasCode(items []finding, code string) bool {
	for _, item := range items {
		if item.Code == code {
			return true
		}
	}
	return false
}

func hasOp(items []action, op string) bool {
	for _, item := range items {
		if item.Op == op {
			return true
		}
	}
	return false
}

func containsAll(got []string, want ...string) bool {
	have := map[string]bool{}
	for _, item := range got {
		have[item] = true
	}
	for _, item := range want {
		if !have[item] {
			return false
		}
	}
	return true
}

func jsonContains(t *testing.T, rep report, needle string) bool {
	t.Helper()
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Contains(raw, []byte(needle))
}
