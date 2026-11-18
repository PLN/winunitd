package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/version"
)

const (
	legacy050 = "{CECB8AEC-2F03-4697-8924-3E6BB832A7C1}"
	legacy060 = "{09120BB5-3A58-4A2B-B6E9-09173A441709}"
)

// Only a major upgrade over a released package with the shutdown seal reads
// unit state. Repair and upgrades from the fixed package keep maintenance alone.
func TestLegacyStopReleaseScope(t *testing.T) {
	for _, tc := range []struct {
		mode, upgrade, want string
	}{
		{modeUpgrade, legacy050, "0.5.0"},
		{modeUpgrade, strings.ToLower(strings.Trim(legacy060, "{}")), "0.6.0"},
		{modeUpgrade, "{00000000-0000-0000-0000-000000000001};" + legacy060, "0.6.0"},
		{modeUpgrade, "{" + version.ProductCode + "}", ""},
		{modeUpgrade, "", ""},
		{modeRepair, legacy050, ""},
		{modeUninstall, legacy050, ""},
		{modeInstall, legacy050, ""},
	} {
		if got := legacyStopRelease(tc.mode, tc.upgrade); got != tc.want {
			t.Errorf("%s %q = %q, want %q", tc.mode, tc.upgrade, got, tc.want)
		}
	}
	if _, listed := legacyStopProducts[strings.ToUpper(version.ProductCode)]; listed {
		t.Fatal("this package's own ProductCode is listed as a legacy manager")
	}
}

const (
	execStopUnit = "[Service]\nExecStart=C:\\Tools\\api.exe\nExecStop=C:\\Tools\\drain.exe\n"
	simpleUnit   = "[Service]\nExecStart=C:\\Tools\\worker.exe\n"
	invalidUnit  = "[Service]\nExecStart=relative.exe\n"
)

func snapshotOf(units ...protocol.UnitSnapshot) *protocol.SnapshotResult {
	return &protocol.SnapshotResult{Machine: protocol.MachineStatus{State: "running"}, Units: units}
}

func unitsDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func requireText(t *testing.T, err error, want, unwanted []string) {
	t.Helper()
	if err == nil {
		t.Fatal("upgrade was admitted")
	}
	msg := err.Error()
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Errorf("refusal lacks %q: %s", w, msg)
		}
	}
	for _, u := range unwanted {
		if strings.Contains(msg, u) {
			t.Errorf("refusal contains %q: %s", u, msg)
		}
	}
}

// The reported layout: active ExecStop services are refused by name with the
// winctl instruction; active simple services, inactive ExecStop services,
// other kinds and native proxies without ExecStop do not block.
func TestLegacyStopConflictNamesExecStopUnits(t *testing.T) {
	snap := snapshotOf(
		protocol.UnitSnapshot{Name: "api.service", ActiveState: "active"},
		protocol.UnitSnapshot{Name: "queue.service", ActiveState: "activating"},
		protocol.UnitSnapshot{Name: "worker.service", ActiveState: "active"},
		protocol.UnitSnapshot{Name: "idle.service", ActiveState: "inactive"},
		protocol.UnitSnapshot{Name: "broken.service", ActiveState: "failed"},
		protocol.UnitSnapshot{Name: "nightly.timer", ActiveState: "active"},
		protocol.UnitSnapshot{Name: "spooler.service", ActiveState: "active", NativeProxy: &protocol.NativeProxyStatus{Kind: "scm"}},
	)
	dir := unitsDir(t, map[string]string{
		"api.service":    execStopUnit,
		"queue.service":  execStopUnit,
		"worker.service": simpleUnit,
		"idle.service":   execStopUnit,
		"broken.service": execStopUnit,
	})
	requireText(t, legacyStopConflict("0.5.0", snap, nil, dir),
		[]string{"preflight conflict", "0.5.0", "api.service (active)", "queue.service (activating)", "winctl stop", "retry", "Nothing was changed"},
		[]string{"worker.service", "idle.service", "broken.service", "nightly.timer", "spooler.service"})

	simple := snapshotOf(snap.Units[2], snap.Units[3], snap.Units[6])
	if err := legacyStopConflict("0.6.0", simple, nil, dir); err != nil {
		t.Fatalf("unaffected units blocked the upgrade: %v", err)
	}
}

// Revisions are sequence numbers, so an owned invocation captured under an
// earlier one may hold an ExecStop the current file lacks. Native proxies and
// non-service units launch no helper and are not flagged for a graph change.
func TestLegacyStopConflictStaleRevision(t *testing.T) {
	dir := unitsDir(t, map[string]string{"worker.service": simpleUnit, "fresh.service": simpleUnit})
	current := func(name, invocation string) protocol.UnitSnapshot {
		return protocol.UnitSnapshot{Name: name, ActiveState: "active", ConfigRevision: "ns/7", InvocationConfigRevision: invocation}
	}
	proxy := current("spooler.service", "ns/3")
	proxy.NativeProxy = &protocol.NativeProxyStatus{Kind: "scm"}
	admitted := snapshotOf(current("fresh.service", "ns/7"), current("nightly.timer", "ns/3"), current("all.target", "ns/3"), proxy)
	if err := legacyStopConflict("0.6.0", admitted, nil, dir); err != nil {
		t.Fatalf("current invocations, other kinds or a proxy blocked the upgrade: %v", err)
	}
	stale := snapshotOf(current("worker.service", "ns/3"), current("fresh.service", "ns/7"))
	requireText(t, legacyStopConflict("0.6.0", stale, nil, dir),
		[]string{"earlier configuration", "worker.service (active, started under revision ns/3", "winctl stop", "Nothing was changed"},
		[]string{"fresh.service"})
}

// Without a readable unit state or definition the upgrade is declined rather
// than assumed safe, and the parser's own diagnostics are kept.
func TestLegacyStopConflictFailsClosed(t *testing.T) {
	snap := snapshotOf(
		protocol.UnitSnapshot{Name: "gone.service", ActiveState: "active"},
		protocol.UnitSnapshot{Name: "bad.service", ActiveState: "active"},
	)
	dir := unitsDir(t, map[string]string{"bad.service": invalidUnit})
	requireText(t, legacyStopConflict("0.5.0", snap, nil, dir),
		[]string{"could not be confirmed", "gone.service (active: ", "bad.service (active: ", "bad.service:2: error:"}, nil)
}

// Without ordinary control the dedicated path is kept when no service file
// could have ExecStop, and otherwise refused with names, read-only, pointing
// at restoring ordinary control rather than stopping the manager.
func TestLegacyStopSnapshotUnavailable(t *testing.T) {
	busy := errors.New("pipe busy")
	for _, dir := range []string{
		unitsDir(t, map[string]string{"worker.service": simpleUnit, "nightly.timer": "[Timer]\nOnCalendar=daily\n", "notes.txt": "x"}),
		filepath.Join(t.TempDir(), "missing"),
	} {
		if err := legacyStopConflict("0.5.0", nil, busy, dir); err != nil {
			t.Fatalf("snapshot failure blocked units without ExecStop: %v", err)
		}
	}
	notService := []string{"Stop-Service", "sc stop", "stop the winunitd service", "net stop"}
	dir := unitsDir(t, map[string]string{"worker.service": simpleUnit, "api.service": execStopUnit, "bad.service": invalidUnit})
	requireText(t, legacyStopConflict("0.5.0", nil, busy, dir),
		[]string{"pipe busy", "Unit files with ExecStop: api.service.", "could not be classified: bad.service (", "bad.service:2: error:", "ordinary control", "winctl snapshot", "winctl stop", "Nothing was changed"},
		append([]string{"worker.service"}, notService...))
	requireText(t, legacyStopConflict("0.5.0", nil, nil, dir), []string{"no unit state returned", "api.service"}, nil)

	crowded := t.TempDir()
	for i := 0; i <= legacyScanUnitFiles; i++ {
		if err := os.WriteFile(filepath.Join(crowded, fmt.Sprintf("t%04d.timer", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	requireText(t, legacyStopConflict("0.5.0", nil, busy, crowded),
		[]string{"could not be inspected", "more than 1024 unit files", "winctl snapshot", "Nothing was changed"}, notService)
}
