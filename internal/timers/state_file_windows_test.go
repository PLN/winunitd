package timers

import (
	"golang.org/x/sys/windows"
	"testing"
)

func TestWindowsTimerStoreLockedReplacement(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := crashState("pending")
	if err := store.Save("work.timer", old); err != nil {
		t.Fatal(err)
	}
	path, err := windows.UTF16PtrFromString(store.path("work.timer"))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if handle != 0 {
			windows.CloseHandle(handle)
		}
	}()
	if err := store.Save("work.timer", crashState("success")); err == nil {
		t.Fatal("locked destination reported success")
	}
	if got, err := store.LoadChecked("work.timer"); err != nil || got != old {
		t.Fatalf("locked replacement changed state: %+v %v", got, err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	handle = 0
	if err := store.Save("work.timer", crashState("success")); err != nil {
		t.Fatal(err)
	}
	if got, err := store.LoadChecked("work.timer"); err != nil || got != crashState("success") {
		t.Fatal("replacement did not recover after handle release")
	}
}
