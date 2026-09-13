//go:build windows

// Command msi-check provides embedded installer preflight and policy rollback.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "fail" {
		os.Exit(1) // Explicit fault injection for the disposable installer tests.
	}
	args := os.Args[1:]
	var err error
	if len(args) > 0 && strings.HasPrefix(args[0], "policy-") {
		err = policy(args)
	} else {
		err = check(args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "winunitd installer:", err)
		os.Exit(1)
	}
}

func check(args []string) error {
	return checkRegistration(args, true)
}

func checkRegistration(args []string, requireStopped bool) error {
	if len(args) != 4 || args[0] != "check" {
		return fmt.Errorf("invalid preflight arguments")
	}
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX64, 0)
	if err != nil {
		return err
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return err
	}
	installDir, dataDir := filepath.Join(programFiles, "winunitd"), filepath.Join(programData, "winunitd")
	if !strings.EqualFold(filepath.Clean(args[1]), installDir) || !strings.EqualFold(filepath.Clean(args[2]), dataDir) {
		return fmt.Errorf("beta installation requires the standard Program Files and ProgramData directories")
	}
	for _, dir := range []string{installDir, dataDir} {
		if err := protectedDirectory(dir); err != nil {
			return err
		}
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService("winunitd")
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return err
	}
	defer s.Close()
	if args[3] == "" {
		return fmt.Errorf("an unmanaged winunitd service exists; back up and remove its registration before installing")
	}
	cfg, err := s.Config()
	if err != nil {
		return err
	}
	argv, err := windows.DecomposeCommandLine(cfg.BinaryPathName)
	if err != nil || len(argv) != 3 || !strings.EqualFold(argv[0], filepath.Join(installDir, "winunitd.exe")) || argv[1] != "--base-dir" || !strings.EqualFold(filepath.Clean(argv[2]), dataDir) || cfg.ServiceStartName != "LocalSystem" {
		return fmt.Errorf("service registration differs from this package; restore it before servicing")
	}
	status, err := s.Query()
	if err != nil {
		return err
	}
	if requireStopped && (status.State != svc.Stopped || status.ProcessId != 0) {
		return fmt.Errorf("stop winunitd and wait for it to exit before repair, upgrade, or uninstall")
	}
	return nil
}

// Reject redirected roots before any MSI file or permission mutation. Existing
// product directories must already be administrator-controlled; do not adopt
// a user-created ProgramData tree by merely changing its root permissions.
func protectedDirectory(dir string) error {
	for p := dir; ; p = filepath.Dir(p) {
		name, err := windows.UTF16PtrFromString(p)
		if err != nil {
			return err
		}
		attrs, err := windows.GetFileAttributes(name)
		if err == nil && (attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0) {
			return fmt.Errorf("installation directories must be ordinary directories")
		}
		if err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return err
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return err
	}
	return protectedSecurity(sd)
}

func protectedSecurity(sd *windows.SECURITY_DESCRIPTOR) error {
	trusted := func(sid *windows.SID) bool {
		return sid != nil && (sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
	}
	owner, _, err := sd.Owner()
	if err != nil || !trusted(owner) {
		return fmt.Errorf("existing product directory must be owned by SYSTEM or Administrators")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("existing product directory requires a restrictive DACL")
	}
	const fileDeleteChild = 0x0040
	const write = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | fileDeleteChild | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("unsupported product directory ACL")
		}
		// Include inherited-only grants: they can make privileged child files writable.
		if uint32(ace.Mask)&uint32(write) != 0 && !trusted((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
			return fmt.Errorf("existing product directory permits non-administrator writes")
		}
	}
	return nil
}
