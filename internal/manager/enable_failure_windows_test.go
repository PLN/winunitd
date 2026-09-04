//go:build windows

package manager

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDisableReportsLockedEnableLink(t *testing.T) {
	m := testManager(t, map[string]string{"foo.service": "[Service]\nExecStart=C:\\Tools\\foo.exe\nWorkingDirectory=C:\\Tools\n"})
	if _, err := m.Enable("foo.service"); err != nil {
		t.Fatal(err)
	}
	path := m.cfg.EnabledPath(DefaultTarget, "foo.service")
	wp, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(wp, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if _, err := m.Disable("foo.service"); err == nil {
		t.Fatal("disable must report the sharing violation")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	st, err := m.Status("foo.service")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Unit.Enabled {
		t.Fatal("surviving link must remain enabled in memory")
	}
}
