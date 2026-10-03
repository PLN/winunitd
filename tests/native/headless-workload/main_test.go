package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/runtime"
)

const backgroundUnit = "[Unit]\nDescription=Headless background workload\n[Service]\nType=simple\n" +
	"ExecStart=C:\\Tools\\headless-workload.exe serve\nRestart=always\nRestartSec=1s\n[Install]\nWantedBy=default.target\n"

func runProvision(role string, args ...string) (int, Provisioned, string) {
	var out, errOut bytes.Buffer
	code := provision(role, args, &out, &errOut)
	var p Provisioned
	_ = json.Unmarshal(out.Bytes(), &p)
	return code, p, errOut.String()
}

func TestProvisionUnitStagesOnlyTheUnitAndItsLink(t *testing.T) {
	src := filepath.Join(t.TempDir(), "background.service")
	if err := os.WriteFile(src, []byte(backgroundUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "winunitd")
	code, p, errOut := runProvision("provision-unit", "--unit-file", src, "--target-base", target)
	if code != 0 {
		t.Fatalf("provision-unit: %d %s", code, errOut)
	}
	want := []string{filepath.Join(target, "units", "background.service"), filepath.Join(target, "enabled", "default.target", "background.service")}
	if !slices.Equal(p.Files, want) {
		t.Fatalf("files %v", p.Files)
	}
	var found []string
	_ = filepath.WalkDir(target, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = append(found, path)
		}
		return err
	})
	slices.Sort(found)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Fatalf("the target holds more than the unit and its link: %v", found)
	}
	if link, _ := os.ReadFile(want[0]); string(link) != "background.service\n" {
		t.Fatalf("enable link %q", link)
	}
	// The staged files are what a manager on that base loads and enables.
	m, err := manager.New(manager.Config{BaseDir: target, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if rel, err := m.Reload(); err != nil || len(rel.Errors) > 0 {
		t.Fatalf("reload %+v %v", rel, err)
	}
	// Staging again with the same unit is accepted; a different one is not.
	if code, _, errOut := runProvision("provision-unit", "--unit-file", src, "--target-base", target); code != 0 {
		t.Fatalf("restage: %d %s", code, errOut)
	}
	if err := os.WriteFile(src, []byte(strings.Replace(backgroundUnit, "RestartSec=1s", "RestartSec=2s", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runProvision("provision-unit", "--unit-file", src, "--target-base", target); code != 1 {
		t.Fatal("a different unit replaced the staged one")
	}
}

func TestProvisionUnitRejectsUnloadableUnits(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "broken.service")
	if err := os.WriteFile(bad, []byte("[Service]\nType=simple\nBogusDirective=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "winunitd")
	if code, _, _ := runProvision("provision-unit", "--unit-file", bad, "--target-base", target); code != 1 {
		t.Fatal("an unloadable unit was staged")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("a refused unit left files in the target")
	}
	notService := filepath.Join(dir, "tick.timer")
	if err := os.WriteFile(notService, []byte("[Timer]\nOnBootSec=1s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--unit-file", notService, "--target-base", target},
		{"--unit-file", "relative.service", "--target-base", target},
		{"--unit-file", bad},
	} {
		if code, _, _ := runProvision("provision-unit", args...); code != 2 {
			t.Errorf("%q accepted", args)
		}
	}
}

func TestProvisionLingerWritesTheExplicitGrant(t *testing.T) {
	base := t.TempDir()
	sid := "S-1-5-21-1-2-3-1001"
	code, p, errOut := runProvision("provision-linger", "--system-base", base, "--sid", sid, "--name", "alice")
	if code != 0 || len(p.Files) != 1 {
		t.Fatalf("provision-linger: %d %v %s", code, p, errOut)
	}
	got, err := manager.OpenLingerStore(filepath.Join(base, "linger")).Get(sid)
	if err != nil || got != (runtime.LingerRecord{SID: sid, Name: "alice"}) {
		t.Fatalf("stored grant %+v %v", got, err)
	}
	for _, args := range [][]string{
		{"--system-base", base, "--sid", "S-1-5-18"},
		{"--system-base", base, "--sid", "alice"},
		{"--system-base", "relative", "--sid", sid},
		{"--system-base", base, "--sid", sid, "--credential-uri", "x"},
	} {
		if code, _, _ := runProvision("provision-linger", args...); code != 2 {
			t.Errorf("%q accepted", args)
		}
	}
}
