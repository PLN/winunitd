package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/servicing"
)

func TestFloorPreflight(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataDir, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	clean := false
	build := servicing.Build{Version: "0.1.0-alpha", Commit: "abc", Modified: &clean}
	for _, mode := range []string{modeInstall, modeRepair, modeUpgrade, modeUninstall} {
		if err := floorPreflight(mode, dataDir, build); err != nil {
			t.Fatalf("%s without a floor: %v", mode, err)
		}
	}
	if err := servicing.WriteFloor(servicing.FloorPath(dataDir), &servicing.Floor{Schema: 1, MinVersion: "0.1.0-alpha", RequireCleanBuild: true}); err != nil {
		t.Fatal(err)
	}
	if err := floorPreflight(modeRepair, dataDir, build); err != nil {
		t.Fatalf("satisfied floor: %v", err)
	}
	if err := servicing.WriteFloor(servicing.FloorPath(dataDir), &servicing.Floor{Schema: 1, MinVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{modeInstall, modeRepair, modeUpgrade} {
		err := floorPreflight(mode, dataDir, build)
		if err == nil || err.Error() != "preflight conflict: compatibility floor: below compatibility floor: version 0.1.0-alpha is below 0.2.0" {
			t.Fatalf("%s below the floor: %v", mode, err)
		}
	}
	if err := floorPreflight(modeUninstall, dataDir, build); err != nil {
		t.Fatalf("uninstall below the floor: %v", err)
	}
	// A record that cannot be decoded is not a missing floor.
	if err := os.WriteFile(servicing.FloorPath(dataDir), []byte(`{"schema":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := floorPreflight(modeUpgrade, dataDir, build); err == nil || !strings.Contains(err.Error(), "preflight conflict: compatibility floor: compatibility floor record is unusable") {
		t.Fatalf("unusable record: %v", err)
	}
}
