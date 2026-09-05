//go:build windows

package registry

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func TestWindowsOpenWatchFiresOnHKCUSet(t *testing.T) {
	if err := UserHiveWatchOK(); err != nil {
		t.Skip(err.Error())
	}
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

func closeTestWatch(t *testing.T) *winWatch {
	t.Helper()
	path := fmt.Sprintf(`Software\winunitd\close-tests\watch-%d-%d`, os.Getpid(), time.Now().UnixNano())
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, path) })
	key, err := ParseKey(`HKCU\` + path)
	if err != nil {
		t.Fatal(err)
	}
	watch, err := OpenWatch(key)
	if err != nil {
		t.Fatal(err)
	}
	w := watch.(*winWatch)
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestRegistryCloseRetainsProtectedEvent(t *testing.T) {
	w := closeTestWatch(t)
	h := w.event
	const protectFromClose = 0x2
	if err := windows.SetHandleInformation(h, protectFromClose, protectFromClose); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.SetHandleInformation(h, protectFromClose, 0) })
	if err := w.Close(); err == nil {
		t.Fatal("protected event close reported success")
	}
	if w.event != h || w.key != 0 {
		t.Fatal("failed close did not retain unfinished handle")
	}
	select {
	case <-w.done:
	default:
		t.Fatal("Close returned before waiter exited")
	}
	if err := windows.SetHandleInformation(h, protectFromClose, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal("close retry", err)
	}
	if w.event != 0 {
		t.Fatal("retry did not release event")
	}
}

func TestRegistryConcurrentCloseDrainsWaiter(t *testing.T) {
	w := closeTestWatch(t)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- w.Close() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if w.key != 0 || w.event != 0 {
		t.Fatal("concurrent close left handles")
	}
	select {
	case <-w.done:
	default:
		t.Fatal("waiter still running")
	}
}
