//go:build windows

package registry

import (
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func TestWindowsOpenWatchFiresOnHKCUSet(t *testing.T) {
	path := fmt.Sprintf(`Software\winunitd\t1-registry\watch-%d-%d`, os.Getpid(), time.Now().UnixNano())
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Skipf("cannot open HKCU: %v", err)
	}
	_ = k.Close()
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, path) })

	key, err := ParseKey(`HKCU\` + path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWatch(key)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	hk, err := registry.OpenKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	if err := hk.SetStringValue("v", "1"); err != nil {
		t.Fatal(err)
	}
	_ = hk.Close()

	select {
	case <-w.C():
	case <-time.After(3 * time.Second):
		t.Fatal("HKCU watch did not fire after SetStringValue")
	}
}
