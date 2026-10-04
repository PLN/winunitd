//go:build windows

package main

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"github.com/PLN/winunitd/internal/protocol"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// systemStopped positively verifies that the system manager and every user
// manager it brokers have stopped: the winunitd service, when registered, is
// stopped with no process; nothing serves the system control or maintenance
// endpoint; and no winunitd.exe process runs except this one. Anything it
// cannot verify counts as running.
func systemStopped() error {
	if err := serviceStopped(); err != nil {
		return err
	}
	if err := endpointsUnserved(); err != nil {
		return err
	}
	return onlyThisDaemonProcess()
}

func serviceStopped() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("query the service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService("winunitd")
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("query the winunitd service: %w", err)
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return fmt.Errorf("query the winunitd service: %w", err)
	}
	if st.State != svc.Stopped || st.ProcessId != 0 {
		return errors.New("the winunitd service is not stopped")
	}
	return nil
}

// endpointsUnserved lists the pipe namespace, which does not connect to or
// consume a server instance.
func endpointsUnserved() error {
	served := map[string]bool{}
	for _, name := range []string{protocol.DefaultPipeName, protocol.MaintenancePipeName} {
		served[strings.ToLower(strings.TrimPrefix(name, `\\.\pipe\`))] = false
	}
	pattern, err := windows.UTF16PtrFromString(`\\.\pipe\*`)
	if err != nil {
		return err
	}
	var data windows.Win32finddata
	h, err := windows.FindFirstFile(pattern, &data)
	if err != nil {
		return fmt.Errorf("list named pipes: %w", err)
	}
	defer windows.FindClose(h)
	for {
		name := strings.ToLower(windows.UTF16ToString(data.FileName[:]))
		if _, ok := served[name]; ok {
			return fmt.Errorf(`\\.\pipe\%s is being served`, name)
		}
		err := windows.FindNextFile(h, &data)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("list named pipes: %w", err)
		}
	}
}

func onlyThisDaemonProcess() error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return fmt.Errorf("list processes: %w", err)
	}
	defer windows.CloseHandle(snap)
	self := windows.GetCurrentProcessId()
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID != self && strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "winunitd.exe") {
			return fmt.Errorf("winunitd.exe process %d is running", entry.ProcessID)
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("list processes: %w", err)
	}
	return nil
}
