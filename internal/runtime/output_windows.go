//go:build windows

package runtime

import (
	"fmt"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const protectHandleFromClose = 0x00000002

var getHandleInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")

// ownedOutput keeps native close ownership independent of os.File. File.Close
// makes the Go wrapper unusable even when CloseHandle fails; its next call only
// reports ErrClosed. Capture and process cleanup share this one close owner.
type ownedOutput struct {
	file                *os.File
	mu                  sync.Mutex
	handle              windows.Handle
	fileClosed          bool
	temporaryProtection bool
}

func newOwnedOutput(handle windows.Handle, name string) *ownedOutput {
	return &ownedOutput{file: os.NewFile(uintptr(handle), name), handle: handle}
}

func (o *ownedOutput) Read(data []byte) (int, error) {
	if o == nil {
		return 0, os.ErrClosed
	}
	return o.file.Read(data)
}

func (o *ownedOutput) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.handle == 0 {
		return nil
	}
	if !o.fileClosed {
		// Keep the handle alive while File.Close joins pending Go reads. Stop
		// terminates the writers first. A read-side final release can lose the native close
		// error inside Go's poller. Preserve any pre-existing protection.
		var flags uint32
		ok, _, err := getHandleInformation.Call(uintptr(o.handle), uintptr(unsafe.Pointer(&flags)))
		if ok == 0 {
			return fmt.Errorf("query output handle flags: %w", err)
		}
		if flags&protectHandleFromClose == 0 {
			if err := windows.SetHandleInformation(o.handle, protectHandleFromClose, protectHandleFromClose); err != nil {
				return fmt.Errorf("retain output during reader close: %w", err)
			}
			o.temporaryProtection = true
		}
		_ = o.file.Close() // Expected native close rejection; this owner releases it below.
		o.fileClosed = true
	}
	if o.temporaryProtection {
		if err := windows.SetHandleInformation(o.handle, protectHandleFromClose, 0); err != nil {
			return fmt.Errorf("release output close protection: %w", err)
		}
		o.temporaryProtection = false
	}
	if err := windows.CloseHandle(o.handle); err != nil {
		return fmt.Errorf("close output handle: %w", err)
	}
	o.handle = 0
	return nil
}
