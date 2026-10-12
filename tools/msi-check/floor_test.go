package main

import (
	"os"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/capability"
	"github.com/PLN/winunitd/internal/servicing"
	"github.com/PLN/winunitd/internal/servicing/servicingtest"
	"github.com/PLN/winunitd/internal/version"
)

func TestFloorPreflight(t *testing.T) {
	dataDir := servicingtest.DataRoot(t)
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

// The helper refuses a modified build, or one without known source state,
// under a clean-build floor before it changes anything; uninstall proceeds.
func TestFloorPreflightRefusesModifiedAndUnknownBuilds(t *testing.T) {
	dataDir := servicingtest.DataRoot(t)
	if err := servicing.WriteFloor(servicing.FloorPath(dataDir), &servicing.Floor{Schema: 1, RequireCleanBuild: true}); err != nil {
		t.Fatal(err)
	}
	clean, modified := false, true
	features := capability.SystemFeatures()
	if err := floorPreflight(modeRepair, dataDir, servicing.BuildFrom(version.BuildInfo{Version: "0.1.0-alpha", Commit: "abc", Modified: &clean}, features)); err != nil {
		t.Fatalf("clean build: %v", err)
	}
	for name, c := range map[string]struct {
		info version.BuildInfo
		want string
	}{
		"modified":      {version.BuildInfo{Version: "0.1.0-alpha", Commit: "abc", Modified: &modified}, "build is from a modified source tree"},
		"no revision":   {version.BuildInfo{Version: "0.1.0-alpha"}, "build has no source revision"},
		"unknown state": {version.BuildInfo{Version: "0.1.0-alpha", Commit: "abc"}, "build source state is unknown"},
	} {
		b := servicing.BuildFrom(c.info, features)
		for _, mode := range []string{modeInstall, modeRepair, modeUpgrade} {
			err := floorPreflight(mode, dataDir, b)
			if err == nil || err.Error() != "preflight conflict: compatibility floor: below compatibility floor: "+c.want {
				t.Errorf("%s %s: %v", name, mode, err)
			}
		}
		if err := floorPreflight(modeUninstall, dataDir, b); err != nil {
			t.Errorf("%s uninstall: %v", name, err)
		}
	}
}
