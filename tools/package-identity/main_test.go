package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
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

// testPayload is a complete set of checked binaries of one build.
func testPayload(helper executable) map[string]executable {
	return map[string]executable{
		"winunitd.exe":       linked(testInfo(nil), "0.1.0-alpha"),
		"winctl.exe":         linked(testInfo(nil), "0.1.0-alpha"),
		"winunit-notify.exe": {info: testInfo(nil)},
		helperKey:            helper,
	}
}

func TestCheckRequiresOneBuild(t *testing.T) {
	payload := testPayload
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
	mixed["winunit-notify.exe"] = linked(testInfo(nil), "0.0.9")
	if _, err := check(testManifest(false), "0.1.0-alpha", mixed, "abc", false); err == nil {
		t.Error("a payload binary of another release accepted")
	}
	for _, name := range []string{"winunitd.exe", "winctl.exe"} {
		unlinked := payload(linked(testInfo(nil), "0.1.0-alpha"))
		unlinked[name] = executable{info: testInfo(nil)}
		if _, err := check(testManifest(false), "0.1.0-alpha", unlinked, "abc", false); err == nil || !strings.Contains(err.Error(), "does not link a release value") {
			t.Errorf("%s without a linked release: %v", name, err)
		}
	}
	// Every payload executable and the helper are checked, and nothing else.
	for _, name := range []string{"winunitd.exe", "winctl.exe", "winunit-notify.exe", helperKey} {
		missing := payload(linked(testInfo(nil), "0.1.0-alpha"))
		delete(missing, name)
		if _, err := check(testManifest(false), "0.1.0-alpha", missing, "abc", false); err == nil || !strings.Contains(err.Error(), name+" was not checked") {
			t.Errorf("without %s: %v", name, err)
		}
	}
	extra := payload(linked(testInfo(nil), "0.1.0-alpha"))
	extra["other.exe"] = linked(testInfo(nil), "0.1.0-alpha")
	if _, err := check(testManifest(false), "0.1.0-alpha", extra, "abc", false); err == nil {
		t.Error("a binary outside the package accepted")
	}
	if _, err := check(testManifest(false), "0.2.0", payload(linked(testInfo(nil), "0.2.0")), "abc", false); err == nil {
		t.Error("a manifest of another release accepted")
	}
	short := testManifest(false)
	short.Commit = testCommit[:12]
	if _, err := check(short, "0.1.0-alpha", payload(linked(testInfo(nil), "0.1.0-alpha")), "abc", false); err == nil {
		t.Error("an abbreviated revision accepted")
	}
}

// The manifest lists exactly the installer's payload, each file once under
// its own name.
func TestPayloadArtifacts(t *testing.T) {
	full := func() []artifact {
		var out []artifact
		for _, f := range installerPayload {
			out = append(out, artifact{Name: f.name, SHA256: strings.Repeat("a", 64)})
		}
		return out
	}
	m := testManifest(false)
	m.Artifacts = full()
	if listed, err := payloadArtifacts(m); err != nil || len(listed) != len(installerPayload) {
		t.Fatalf("complete payload: %v %v", listed, err)
	}
	for _, f := range installerPayload {
		m.Artifacts = slices.DeleteFunc(full(), func(a artifact) bool { return a.Name == f.name })
		if _, err := payloadArtifacts(m); err == nil || !strings.Contains(err.Error(), "omits "+f.name) {
			t.Errorf("omitted %s: %v", f.name, err)
		}
	}
	for _, name := range []string{
		"winunitd.exe", "WinUnitD.exe", "WINCTL.EXE", "winunitd.exe.", "winunitd.exe ", "winunitd.exe::$DATA", "winunitd",
		"bin/winunitd.exe", `bin\winunitd.exe`, `..\winunitd.exe`, "C:winunitd.exe", "msi-check.exe", "extra.exe", "notes.txt", "",
	} {
		m.Artifacts = append(full(), artifact{Name: name, SHA256: strings.Repeat("a", 64)})
		if _, err := payloadArtifacts(m); err == nil {
			t.Errorf("an added %q accepted", name)
		}
		// In place of the file it aliases, too.
		m.Artifacts = full()
		m.Artifacts[0].Name = name
		if name != "winunitd.exe" {
			if _, err := payloadArtifacts(m); err == nil {
				t.Errorf("%q in place of winunitd.exe accepted", name)
			}
		}
	}
}

func TestCheckModifiedTreeIsDevelopmentOnly(t *testing.T) {
	modified := func(s map[string]string) { s["vcs.modified"] = "true" }
	binaries := map[string]executable{
		"winunitd.exe":       linked(testInfo(modified), "0.1.0-alpha"),
		"winctl.exe":         linked(testInfo(modified), "0.1.0-alpha"),
		"winunit-notify.exe": {info: testInfo(modified)},
		helperKey:            linked(testInfo(modified), "0.1.0-alpha"),
	}
	if _, err := check(testManifest(true), "0.1.0-alpha", binaries, "abc", false); err == nil || !strings.Contains(err.Error(), "development only") {
		t.Fatalf("modified tree without -development: %v", err)
	}
	id, err := check(testManifest(true), "0.1.0-alpha", binaries, "abc", true)
	if err != nil || id.Admissible || !id.Modified {
		t.Fatalf("development identity %+v %v", id, err)
	}
	// Development mode still requires one build.
	binaries[helperKey] = linked(testInfo(func(s map[string]string) { modified(s); s["vcs.revision"] = strings.Repeat("e", 40) }), "0.1.0-alpha")
	if _, err := check(testManifest(true), "0.1.0-alpha", binaries, "abc", true); err == nil {
		t.Fatal("development mode accepted a helper from another revision")
	}
}

// writeManifest writes m beside the payload files in dir.
func writeManifest(t *testing.T, dir, file string, m buildManifest) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
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
	m := testManifest(false)
	for _, f := range installerPayload {
		content := data
		if !f.executable {
			content = []byte("notices\n")
		}
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		sum, err := fileSHA256(path)
		if err != nil {
			t.Fatal(err)
		}
		m.Artifacts = append(m.Artifacts, artifact{Name: f.name, SHA256: sum})
	}
	if _, err := run(writeManifest(t, dir, "build-manifest.json", m), "0.1.0-alpha", exe, false); err == nil {
		t.Fatal("test binary accepted as a package build")
	}
	changed := m
	changed.Artifacts = slices.Clone(m.Artifacts)
	changed.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	if _, err := run(writeManifest(t, dir, "build-manifest.json", changed), "0.1.0-alpha", exe, false); err == nil || !strings.Contains(err.Error(), "winunitd.exe does not match its build manifest") {
		t.Fatalf("changed payload accepted: %v", err)
	}
	// The daemon file is in the payload directory, but a manifest that
	// omits it is refused before anything is compared.
	omitted := m
	omitted.Artifacts = m.Artifacts[1:]
	if _, err := run(writeManifest(t, dir, "build-manifest.json", omitted), "0.1.0-alpha", exe, false); err == nil || !strings.Contains(err.Error(), "omits winunitd.exe") {
		t.Fatalf("manifest without the daemon: %v", err)
	}
}

// goBuild cross-builds a Windows package from the repository root.
func goBuild(t *testing.T, out, release, pkg string) {
	t.Helper()
	args := []string{"build", "-trimpath", "-buildvcs=true", "-o", out}
	if release != "" {
		args = append(args, "-ldflags", "-X "+versionSymbol+"="+release)
	}
	cmd := exec.Command("go", append(args, pkg)...)
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}
}

// A payload produced by tools/build and a helper built as the packaging
// scripts build it are one build. Omitting any packaged file from the
// manifest, although the file stays in the payload directory, and a daemon
// linked with another release are refused.
func TestRunWithAProducedPayload(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a Windows payload")
	}
	root := filepath.Join("..", "..")
	if err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Run(); err != nil {
		t.Skip("building a payload needs a Git checkout")
	}
	const release = "9.9.9-test"
	dir := t.TempDir()
	cmd := exec.Command("go", "run", "./tools/build", "-goos", "windows", "-goarch", "amd64", "-version", release, "-out", dir)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("payload build: %v\n%s", err, out)
	}
	helper := filepath.Join(t.TempDir(), "msi-check.exe")
	goBuild(t, helper, release, "./tools/msi-check")
	manifestPath := filepath.Join(dir, "build-manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m buildManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	// A modified checkout produces a development payload; the identity
	// checks are the same.
	id, err := run(manifestPath, release, helper, m.Dirty)
	if err != nil || id.Commit != m.Commit || id.Release != release || id.Admissible == m.Dirty {
		t.Fatalf("produced payload: %+v %v", id, err)
	}
	for name, want := range map[string]bool{"winunitd.exe": true, "winctl.exe": true, "winunit-notify.exe": false} {
		got, ok, err := linkedRelease(filepath.Join(dir, name))
		if err != nil || ok != want || (want && got != release) {
			t.Errorf("%s links %q %t %v", name, got, ok, err)
		}
	}
	if got, ok, err := linkedRelease(helper); err != nil || !ok || got != release {
		t.Errorf("helper links %q %t %v", got, ok, err)
	}
	for _, f := range installerPayload {
		omitted := m
		omitted.Artifacts = slices.DeleteFunc(slices.Clone(m.Artifacts), func(a artifact) bool { return a.Name == f.name })
		if _, err := run(writeManifest(t, dir, "omitted.json", omitted), release, helper, m.Dirty); err == nil || !strings.Contains(err.Error(), "omits "+f.name) {
			t.Errorf("manifest without %s: %v", f.name, err)
		}
	}
	// A daemon of another release, listed with its own hash, is refused.
	daemon := filepath.Join(dir, "winunitd.exe")
	goBuild(t, daemon, "0.0.1", "./cmd/winunitd")
	sum, err := fileSHA256(daemon)
	if err != nil {
		t.Fatal(err)
	}
	other := m
	other.Artifacts = slices.Clone(m.Artifacts)
	for i := range other.Artifacts {
		if other.Artifacts[i].Name == "winunitd.exe" {
			other.Artifacts[i].SHA256 = sum
		}
	}
	if _, err := run(writeManifest(t, dir, "other.json", other), release, helper, m.Dirty); err == nil || !strings.Contains(err.Error(), `winunitd.exe links release "0.0.1"`) {
		t.Fatalf("daemon of another release: %v", err)
	}
	if _, _, err := linkedRelease(os.Args[0]); err == nil && runtime.GOOS != "windows" {
		t.Fatal("a non-PE file read as an executable")
	}
}

// The installer packages exactly installerPayload from the payload
// directory, in both package definitions, plus the helper this check
// compares and the token library the packaging script builds and records.
func TestInstallerPayloadMatchesThePackages(t *testing.T) {
	ref := regexp.MustCompile(`(\w+)="\$\(Payload\)\\([^"]+)"`)
	var want []string
	for _, f := range installerPayload {
		want = append(want, f.name)
	}
	slices.Sort(want)
	for _, wxs := range []string{"wix", "beta"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "packaging", wxs, "Package.wxs"))
		if err != nil {
			t.Fatal(err)
		}
		var files, binaries []string
		for _, m := range ref.FindAllStringSubmatch(string(data), -1) {
			switch m[1] {
			case "Source":
				files = append(files, m[2])
			case "SourceFile":
				binaries = append(binaries, m[2])
			default:
				t.Errorf("%s: payload reference %s", wxs, m[0])
			}
		}
		if n := strings.Count(string(data), "$(Payload)"); n != len(files)+len(binaries) {
			t.Errorf("%s: %d payload references, %d understood", wxs, n, len(files)+len(binaries))
		}
		slices.Sort(files)
		slices.Sort(binaries)
		if !slices.Equal(files, want) {
			t.Errorf("%s packages %q, the check requires %q", wxs, files, want)
		}
		if !slices.Equal(binaries, []string{"msi-check.exe", "msi-token.dll"}) {
			t.Errorf("%s package-build binaries %q", wxs, binaries)
		}
	}
}
