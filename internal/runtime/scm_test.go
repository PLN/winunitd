package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServiceIdentity(t *testing.T) {
	if ServiceName != "winunitd" {
		t.Fatalf("ServiceName = %q", ServiceName)
	}
	if DisplayName != "WinUnit Manager" {
		t.Fatalf("DisplayName = %q", DisplayName)
	}
	if ServiceAccount != "LocalSystem" {
		t.Fatalf("ServiceAccount = %q", ServiceAccount)
	}
	if RecoveryActionCount != 3 {
		t.Fatalf("RecoveryActionCount = %d", RecoveryActionCount)
	}
	if RecoveryResetPeriodNever != ^uint32(0) {
		t.Fatalf("RecoveryResetPeriodNever = %d", RecoveryResetPeriodNever)
	}
	if PreshutdownTimeout <= 0 {
		t.Fatal("PreshutdownTimeout unset")
	}
}

func TestEnsureDataDirs(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "winunitd")
	if err := EnsureDataDirs(base); err != nil {
		t.Fatal(err)
	}
	want := []string{"units", "enabled", "journal", "runtime", "linger"}
	if len(DataDirNames) != len(want) {
		t.Fatalf("DataDirNames = %v", DataDirNames)
	}
	for i, name := range want {
		if DataDirNames[i] != name {
			t.Fatalf("DataDirNames[%d] = %q, want %q", i, DataDirNames[i], name)
		}
		path := filepath.Join(base, name)
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !st.IsDir() {
			t.Fatalf("%s is not a directory", path)
		}
	}
	if err := EnsureDataDirs(base); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDataDirsEmptyBase(t *testing.T) {
	if err := EnsureDataDirs(""); err == nil {
		t.Fatal("empty base directory must fail")
	}
}
