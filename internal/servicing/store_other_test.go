//go:build !windows

package servicing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/servicing/servicingtest"
)

// Unix modes stand in for the Windows owner and DACL rules in these tests.

func TestFloorRecordMode(t *testing.T) {
	_, path := floorDir(t)
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("record mode %v %v", fi.Mode(), err)
	}
}

func TestFloorStoreRejectsUntrustedRecords(t *testing.T) {
	_, path := floorDir(t)
	write := func(data string, mode os.FileMode) {
		t.Helper()
		_ = os.Remove(path)
		if err := os.WriteFile(path, []byte(data), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"schema":1,"minVersion":"0.2.0"}`, 0o666)
	if _, err := ReadFloor(path); err == nil {
		t.Error("world-writable record trusted")
	}
	write(`{"schema":1,"minVersion":`, 0o600)
	if _, err := ReadFloor(path); err == nil {
		t.Error("truncated record accepted")
	}
	_ = os.Remove(path)
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"schema":1,"minVersion":"0.0.1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFloor(path); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("record followed a link: %v", err)
	}
}

// Every level of the chain is checked before a record is read, written or
// removed, and before its absence means "no floor".
func TestFloorChainRejectsUnsafeLevels(t *testing.T) {
	floor := &Floor{Schema: 1, MinVersion: "0.1.0"}
	check := func(name, path, want string) {
		t.Helper()
		if f, err := ReadFloor(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: read %+v %v", name, f, err)
		}
		if hold := AdmissionHold(path, Build{Version: "9.9.9"}); !strings.HasPrefix(hold, "compatibility floor record is unusable: ") {
			t.Errorf("%s: hold %q", name, hold)
		}
		if err := WriteFloor(path, floor); err == nil {
			t.Errorf("%s: wrote a record", name)
		}
		if err := RemoveFloor(path); err == nil {
			t.Errorf("%s: removed a record", name)
		}
	}
	chmod := func(dir string, mode os.FileMode) {
		t.Helper()
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	}

	base, path := floorDir(t)
	if err := WriteFloor(path, floor); err != nil {
		t.Fatal(err)
	}
	chmod(filepath.Join(base, "daemon"), 0o777)
	check("writable daemon directory", path, "daemon directory is writable")

	base, path = floorDir(t)
	if err := WriteFloor(path, floor); err != nil {
		t.Fatal(err)
	}
	chmod(base, 0o777)
	check("writable data root", path, "data root is writable")

	// A container others may write must be sticky: otherwise they could
	// remove the data root with the record in it.
	container := servicingtest.Root(t)
	base = filepath.Join(container, "winunitd")
	if err := os.MkdirAll(filepath.Join(base, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	path = FloorPath(base)
	if err := WriteFloor(path, floor); err != nil {
		t.Fatal(err)
	}
	chmod(container, 0o777)
	check("open container", path, "containing directory lets other principals remove")
	chmod(container, 0o777|os.ModeSticky)
	if f, err := ReadFloor(path); err != nil || f == nil {
		t.Fatalf("sticky container: %+v %v", f, err)
	}
	// An unsafe container also blocks reading absence as "no floor".
	chmod(container, 0o777)
	if err := os.RemoveAll(base); err != nil {
		t.Fatal(err)
	}
	check("open container without a data root", path, "containing directory")

	// Linked levels are refused, not followed.
	elsewhere := servicingtest.DataRoot(t)
	if err := WriteFloor(FloorPath(elsewhere), &Floor{Schema: 1, MinVersion: "0.0.1"}); err != nil {
		t.Fatal(err)
	}
	links := servicingtest.Root(t)
	linkedRoot := filepath.Join(links, "root")
	if err := os.Symlink(elsewhere, linkedRoot); err != nil {
		t.Fatal(err)
	}
	check("linked data root", FloorPath(linkedRoot), "data root is a symbolic link")
	realRoot := servicingtest.Root(t)
	if err := os.Symlink(filepath.Join(elsewhere, "daemon"), filepath.Join(realRoot, "daemon")); err != nil {
		t.Fatal(err)
	}
	check("linked daemon directory", FloorPath(realRoot), "daemon directory is a symbolic link")
	linkedContainer := filepath.Join(links, "container")
	if err := os.Symlink(filepath.Dir(elsewhere), linkedContainer); err != nil {
		t.Fatal(err)
	}
	check("linked container", FloorPath(filepath.Join(linkedContainer, filepath.Base(elsewhere))), "containing directory is a symbolic link")
}
