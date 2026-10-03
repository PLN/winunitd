package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func testManifest(dirty bool) buildManifest {
	return buildManifest{Schema: 2, Version: "0.1.0-alpha", Commit: testCommit, Dirty: dirty, Go: "go1.27.1", GOOS: "windows", GOARCH: "amd64"}
}

func testInfo(mutate func(map[string]string)) *debug.BuildInfo {
	settings := map[string]string{
		"-trimpath":    "true",
		"CGO_ENABLED":  "0",
		"GOOS":         "windows",
		"GOARCH":       "amd64",
		"vcs.revision": testCommit,
		"vcs.modified": "false",
	}
	if mutate != nil {
		mutate(settings)
	}
	bi := &debug.BuildInfo{GoVersion: "go1.27.1"}
	for k, v := range settings {
		bi.Settings = append(bi.Settings, debug.BuildSetting{Key: k, Value: v})
	}
	return bi
}

func linked(info *debug.BuildInfo, release string) executable {
	return executable{info: info, release: release, linked: true}
}

func TestCheckRequiresOneBuild(t *testing.T) {
	payload := func(helper executable) map[string]executable {
		daemon := linked(testInfo(nil), "0.1.0-alpha")
		daemon.needsRelease = true
		helper.needsRelease = true
		return map[string]executable{
			"winunitd.exe":         daemon,
			"winctl.exe":           linked(testInfo(nil), "0.1.0-alpha"),
			"winunit-notify.exe":   {info: testInfo(nil)},
			"helper msi-check.exe": helper,
		}
	}
	id, err := check(testManifest(false), "0.1.0-alpha", payload(linked(testInfo(nil), "0.1.0-alpha")), "abc", false)
	if err != nil || !id.Admissible || id.Modified || id.Commit != testCommit || id.Release != "0.1.0-alpha" || id.Helper != "abc" {
		t.Fatalf("one build: %+v %v", id, err)
	}
	for name, helper := range map[string]executable{
		"other revision":  linked(testInfo(func(s map[string]string) { s["vcs.revision"] = strings.Repeat("f", 40) }), "0.1.0-alpha"),
		"modified helper": linked(testInfo(func(s map[string]string) { s["vcs.modified"] = "true" }), "0.1.0-alpha"),
		"no revision":     linked(testInfo(func(s map[string]string) { delete(s, "vcs.revision") }), "0.1.0-alpha"),
		"other release":   linked(testInfo(nil), "0.2.0"),
		"release prefix":  linked(testInfo(nil), "0.1.0-alpha.1"),
		"no release":      {info: testInfo(nil)},
		"cgo":             linked(testInfo(func(s map[string]string) { s["CGO_ENABLED"] = "1" }), "0.1.0-alpha"),
		"untrimmed":       linked(testInfo(func(s map[string]string) { delete(s, "-trimpath") }), "0.1.0-alpha"),
		"other target":    linked(testInfo(func(s map[string]string) { s["GOARCH"] = "arm64" }), "0.1.0-alpha"),
		"other toolchain": linked(&debug.BuildInfo{GoVersion: "go1.26.0", Settings: testInfo(nil).Settings}, "0.1.0-alpha"),
		"no build info":   {release: "0.1.0-alpha", linked: true},
	} {
		if _, err := check(testManifest(false), "0.1.0-alpha", payload(helper), "abc", false); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A payload binary that links another release fails even when it is not
	// one that evaluates the floor.
	mixed := payload(linked(testInfo(nil), "0.1.0-alpha"))
	mixed["winctl.exe"] = linked(testInfo(nil), "0.0.9")
	if _, err := check(testManifest(false), "0.1.0-alpha", mixed, "abc", false); err == nil {
		t.Error("a payload binary of another release accepted")
	}
	unlinked := payload(linked(testInfo(nil), "0.1.0-alpha"))
	unlinked["winunitd.exe"] = executable{info: testInfo(nil), needsRelease: true}
	if _, err := check(testManifest(false), "0.1.0-alpha", unlinked, "abc", false); err == nil {
		t.Error("a daemon without a linked release accepted")
	}
	if _, err := check(testManifest(false), "0.2.0", payload(linked(testInfo(nil), "0.2.0")), "abc", false); err == nil {
		t.Error("a manifest of another release accepted")
	}
	short := testManifest(false)
	short.Commit = testCommit[:12]
	if _, err := check(short, "0.1.0-alpha", payload(linked(testInfo(nil), "0.1.0-alpha")), "abc", false); err == nil {
		t.Error("an abbreviated revision accepted")
	}
	if _, err := check(testManifest(false), "0.1.0-alpha", map[string]executable{"helper msi-check.exe": linked(testInfo(nil), "0.1.0-alpha")}, "abc", false); err == nil {
		t.Error("a helper without payload accepted")
	}
}

func TestCheckModifiedTreeIsDevelopmentOnly(t *testing.T) {
	modified := func(s map[string]string) { s["vcs.modified"] = "true" }
	binaries := map[string]executable{
		"winunitd.exe":         linked(testInfo(modified), "0.1.0-alpha"),
		"helper msi-check.exe": linked(testInfo(modified), "0.1.0-alpha"),
	}
	if _, err := check(testManifest(true), "0.1.0-alpha", binaries, "abc", false); err == nil || !strings.Contains(err.Error(), "development only") {
		t.Fatalf("modified tree without -development: %v", err)
	}
	id, err := check(testManifest(true), "0.1.0-alpha", binaries, "abc", true)
	if err != nil || id.Admissible || !id.Modified {
		t.Fatalf("development identity %+v %v", id, err)
	}
	// Development mode still requires one build.
	binaries["helper msi-check.exe"] = linked(testInfo(func(s map[string]string) { modified(s); s["vcs.revision"] = strings.Repeat("e", 40) }), "0.1.0-alpha")
	if _, err := check(testManifest(true), "0.1.0-alpha", binaries, "abc", true); err == nil {
		t.Fatal("development mode accepted a helper from another revision")
	}
}

// run reads identities from the produced files and their hashes from the
// manifest. A test binary is no Windows package build.
func TestRunReadsProducedBinaries(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	payload := filepath.Join(dir, "winunitd.exe")
	if err := os.WriteFile(payload, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(payload)
	if err != nil {
		t.Fatal(err)
	}
	write := func(m buildManifest) string {
		t.Helper()
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "build-manifest.json")
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	m := testManifest(false)
	m.Artifacts = []artifact{{Name: "winunitd.exe", SHA256: sum}}
	if _, err := run(write(m), "0.1.0-alpha", exe, false); err == nil {
		t.Fatal("test binary accepted as a package build")
	}
	m.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	if _, err := run(write(m), "0.1.0-alpha", exe, false); err == nil || !strings.Contains(err.Error(), "does not match its build manifest") {
		t.Fatalf("changed payload accepted: %v", err)
	}
}

// linkedRelease reads the value -X linked into a real Windows executable,
// and reports a binary that does not link the version variable.
func TestLinkedReleaseOfABuiltExecutable(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Windows executables")
	}
	dir := t.TempDir()
	build := func(out string, args ...string) {
		t.Helper()
		cmd := exec.Command("go", append([]string{"build", "-trimpath", "-o", out}, args...)...)
		cmd.Dir = filepath.Join("..", "..")
		cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, out)
		}
	}
	helper := filepath.Join(dir, "msi-check.exe")
	build(helper, "-ldflags", "-X "+versionSymbol+"=9.9.9-test", "./tools/msi-check")
	if release, ok, err := linkedRelease(helper); err != nil || !ok || release != "9.9.9-test" {
		t.Fatalf("linked release %q %t %v", release, ok, err)
	}
	notify := filepath.Join(dir, "winunit-notify.exe")
	build(notify, "./cmd/winunit-notify")
	if release, ok, err := linkedRelease(notify); err != nil || ok {
		t.Fatalf("notify helper release %q %t %v", release, ok, err)
	}
	if _, _, err := linkedRelease(os.Args[0]); err == nil {
		t.Fatal("a non-PE file read as an executable")
	}
}
