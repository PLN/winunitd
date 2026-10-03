package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/runtime"
)

const testHold = "below compatibility floor: version 0.1.0-alpha is below 0.2.0"

func heldManager(t *testing.T, hold string, launch runtime.Launcher, files map[string]string) *Manager {
	t.Helper()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch, AdmissionHold: hold})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m
}

var heldUnits = map[string]string{
	"worker.service": "[Service]\nExecStart=C:\\Tools\\worker.exe\nRestart=always\n[Install]\nWantedBy=default.target\n",
	"once.service":   "[Service]\nType=oneshot\nExecStart=C:\\Tools\\once.exe\n",
	"tick.timer":     "[Timer]\nOnBootSec=1s\nUnit=once.service\n",
}

func TestAdmissionHoldRefusesEveryStart(t *testing.T) {
	l := &fakeLauncher{}
	m := heldManager(t, testHold, l, heldUnits)
	if _, err := m.Enable("worker.service"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	attempts := map[string]func() error{
		"boot":         func() error { _, err := m.Boot(ctx); return err },
		"start":        func() error { _, err := m.Start(ctx, "worker.service"); return err },
		"restart":      func() error { _, err := m.Restart(ctx, "worker.service"); return err },
		"oneshot":      func() error { _, err := m.Start(ctx, "once.service"); return err },
		"timer":        func() error { _, err := m.Start(ctx, "tick.timer"); return err },
		"target":       func() error { _, err := m.Start(ctx, DefaultTarget); return err },
		"start by RPC": func() error { _, err := m.Start(ctx, "worker"); return err },
	}
	for name, attempt := range attempts {
		err := attempt()
		if err == nil || !strings.Contains(err.Error(), "admission is held: "+testHold) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := len(l.specs()); n != 0 {
		t.Fatalf("held manager launched %d processes", n)
	}
	st, err := m.Status("")
	if err != nil {
		t.Fatal(err)
	}
	if st.Machine == nil || st.Machine.AdmissionHold != testHold || st.Machine.UnitsActive != 0 {
		t.Fatalf("machine status %+v", st.Machine)
	}
	snap, err := m.Snapshot()
	if err != nil || snap.Machine.AdmissionHold != testHold {
		t.Fatalf("snapshot does not report the hold: %+v %v", snap, err)
	}
	// Repair stays possible: configuration reload, stop and disable.
	if _, err := m.Reload(); err != nil {
		t.Fatalf("reload under hold: %v", err)
	}
	if _, err := m.Stop("worker.service"); err != nil {
		t.Fatalf("stop under hold: %v", err)
	}
	if _, err := m.Disable("worker.service"); err != nil {
		t.Fatalf("disable under hold: %v", err)
	}
}

func TestAdmissionHoldAbsentByDefault(t *testing.T) {
	l := &fakeLauncher{}
	m := heldManager(t, "", l, heldUnits)
	if _, err := m.Start(context.Background(), "worker.service"); err != nil {
		t.Fatal(err)
	}
	if len(l.specs()) != 1 {
		t.Fatal("unheld manager did not launch")
	}
	st, err := m.Status("")
	if err != nil || st.Machine.AdmissionHold != "" {
		t.Fatalf("unheld status %+v %v", st, err)
	}
}

func TestAdmissionHoldStopsUserManagerLaunches(t *testing.T) {
	held, heldStarts, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	held.cfg.AdmissionHold = testHold
	dir := t.TempDir()
	held.cfg.LingerDir = dir
	held.store = OpenLingerStore(dir)
	held.cfg.LingerToken = func(rec runtime.LingerRecord) (*runtime.UserToken, error) {
		return &runtime.UserToken{Info: runtime.UserInfo{SID: rec.SID}}, nil
	}
	if err := held.store.Put(runtime.LingerRecord{SID: testSIDB, Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	held.Logon(1)
	held.Reconcile()
	held.StartLingering()
	if heldStarts.Load() != 0 || held.ManagerCount() != 0 {
		t.Fatalf("held host launched %d user managers", heldStarts.Load())
	}
	// Even with the linger record current, its launch admission is refused.
	if _, err := held.refreshLingerRecords(); err != nil {
		t.Fatal(err)
	}
	if err := held.startLinger(runtime.LingerRecord{SID: testSIDB, Name: "bob"}); err == nil || !strings.Contains(err.Error(), "admission is held") {
		t.Fatalf("direct linger start: %v", err)
	}
	if _, err := held.acceptUserLaunch(testSIDA, "interactive", 1, func() bool { return true }); err == nil {
		t.Fatal("launch accepted under hold")
	}
	// The same host without a hold launches the logged-on user's manager.
	open, openStarts, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	open.Logon(1)
	if openStarts.Load() != 1 {
		t.Fatalf("unheld host launched %d user managers", openStarts.Load())
	}
}
