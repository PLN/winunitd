//go:build windows

package timers

import (
	"time"

	"golang.org/x/sys/windows"
)

var (
	modkernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procGetTickCount64 = modkernel32.NewProc("GetTickCount64")
)

func platformSinceBoot() time.Duration {
	r, _, _ := procGetTickCount64.Call()
	return time.Duration(r) * time.Millisecond
}
