package main

import (
	"encoding/json"
	"os"
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
		"-ldflags":     versionFlag + "0.1.0-alpha",
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

func TestCheckRequiresOneBuild(t *testing.T) {
	binaries := func(helper *debug.BuildInfo) map[string]*debug.BuildInfo {
		return map[string]*debug.BuildInfo{"winunitd.exe": testInfo(nil), "winctl.exe": testInfo(nil), "helper msi-check.exe": helper}
	}
	id, err := check(testManifest(false), "0.1.0-alpha", binaries(testInfo(nil)), "abc", false)
	if err != nil || !id.Admissible || id.Modified || id.Commit != testCommit || id.Release != "0.1.0-alpha" || id.Helper != "abc" {
		t.Fatalf("one build: %+v %v", id, err)
	}
	for name, helper := range map[string]*debug.BuildInfo{
		"other revision":    testInfo(func(s map[string]string) { s["vcs.revision"] = strings.Repeat("f", 40) }),
		"modified helper":   testInfo(func(s map[string]string) { s["vcs.modified"] = "true" }),
		"no revision":       testInfo(func(s map[string]string) { delete(s, "vcs.revision") }),
		"default release":   testInfo(func(s map[string]string) { delete(s, "-ldflags") }),
		"other release":     testInfo(func(s map[string]string) { s["-ldflags"] = versionFlag + "0.2.0" }),
		"release set twice": testInfo(func(s map[string]string) { s["-ldflags"] = versionFlag + "0.1.0-alpha " + versionFlag + "0.2.0" }),
		"release prefix":    testInfo(func(s map[string]string) { s["-ldflags"] = versionFlag + "0.1.0-alpha.1" }),
		"cgo":               testInfo(func(s map[string]string) { s["CGO_ENABLED"] = "1" }),
		"untrimmed":         testInfo(func(s map[string]string) { delete(s, "-trimpath") }),
		"other target":      testInfo(func(s map[string]string) { s["GOARCH"] = "arm64" }),
		"other toolchain":   {GoVersion: "go1.26.0", Settings: testInfo(nil).Settings},
	} {
		if _, err := check(testManifest(false), "0.1.0-alpha", binaries(helper), "abc", false); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := check(testManifest(false), "0.2.0", binaries(testInfo(nil)), "abc", false); err == nil {
		t.Error("a manifest of another release accepted")
	}
	short := testManifest(false)
	short.Commit = testCommit[:12]
	if _, err := check(short, "0.1.0-alpha", binaries(testInfo(nil)), "abc", false); err == nil {
		t.Error("an abbreviated revision accepted")
	}
	if _, err := check(testManifest(false), "0.1.0-alpha", map[string]*debug.BuildInfo{"helper msi-check.exe": testInfo(nil)}, "abc", false); err == nil {
		t.Error("a helper without payload accepted")
	}
}

func TestCheckModifiedTreeIsDevelopmentOnly(t *testing.T) {
	modified := func(s map[string]string) { s["vcs.modified"] = "true" }
	binaries := map[string]*debug.BuildInfo{"winunitd.exe": testInfo(modified), "helper msi-check.exe": testInfo(modified)}
	if _, err := check(testManifest(true), "0.1.0-alpha", binaries, "abc", false); err == nil || !strings.Contains(err.Error(), "development only") {
		t.Fatalf("modified tree without -development: %v", err)
	}
	id, err := check(testManifest(true), "0.1.0-alpha", binaries, "abc", true)
	if err != nil || id.Admissible || !id.Modified {
		t.Fatalf("development identity %+v %v", id, err)
	}
	// Development mode still requires one build.
	binaries["helper msi-check.exe"] = testInfo(func(s map[string]string) { modified(s); s["vcs.revision"] = strings.Repeat("e", 40) })
	if _, err := check(testManifest(true), "0.1.0-alpha", binaries, "abc", true); err == nil {
		t.Fatal("development mode accepted a helper from another revision")
	}
}

// run reads the identities from the produced files and their hashes from
// the manifest. A test binary carries no release or revision, so it is not
// a package build.
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
	if _, err := run(write(m), "0.1.0-alpha", exe, false); err == nil || !strings.Contains(err.Error(), "winunitd.exe") && !strings.Contains(err.Error(), "helper") {
		t.Fatalf("test binary accepted as a package build: %v", err)
	}
	m.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	if _, err := run(write(m), "0.1.0-alpha", exe, false); err == nil || !strings.Contains(err.Error(), "does not match its build manifest") {
		t.Fatalf("changed payload accepted: %v", err)
	}
}
