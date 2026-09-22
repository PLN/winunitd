//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

func pathIsReparse(path string, info os.FileInfo) (bool, error) {
	if info != nil && info.Mode()&os.ModeSymlink != 0 {
		return true, nil
	}
	return pathHasReparseAttribute(path)
}

func pathHasReparseAttribute(path string) (bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attrs, err := windows.GetFileAttributes(name)
	if err != nil {
		return false, err
	}
	return attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

// preflightProduct inspects directories, the SCM registration, and
// machine-wide launchers. It does not stop, start, delete, or reconfigure
// the service, and it does not set ACLs or open reparse targets.
func preflightProduct(installDir, dataDir, mode string) error {
	dirs, err := collectDirectoryFacts(installDir, dataDir)
	if err != nil {
		return err
	}
	facts, err := queryServiceFacts()
	if err != nil {
		return err
	}
	pilots := 0
	if mode != modeUninstall {
		n, err := machinePilotCount()
		if err != nil {
			return err
		}
		pilots = n
	}
	return evaluatePreflight(mode, dirs, facts, installDir, dataDir, pilots)
}

func collectDirectoryFacts(installDir, dataDir string) ([]dirFact, error) {
	if err := protectedDirectory(installDir); err != nil {
		return nil, fmt.Errorf("preflight conflict: unsafe directory install: %w", err)
	}
	if err := protectedDirectory(dataDir); err != nil {
		return nil, fmt.Errorf("preflight conflict: unsafe directory data: %w", err)
	}
	facts := make([]dirFact, 0, 1+len(mutableDataDirs))
	bin, err := inspectDirectory("bin", filepath.Join(installDir, "bin"))
	if err != nil {
		return nil, err
	}
	facts = append(facts, bin)
	for _, name := range mutableDataDirs {
		fact, err := inspectDirectory(name, filepath.Join(dataDir, name))
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// inspectDirectory reports one path. A reparse point is returned as
// Reparse and is not opened, so its target ACL is never read or written.
func inspectDirectory(name, path string) (dirFact, error) {
	fact := dirFact{Name: name}
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fact, err
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		fact.Missing = true
		return fact, nil
	}
	if err != nil {
		return fact, err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		fact.Reparse = true
		return fact, nil
	}
	fact.Directory = attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if !fact.Directory {
		return fact, nil
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fact, err
	}
	if err := protectedSecurity(sd); err != nil {
		if ownerRejected(err) {
			fact.TrustedOwn = false
			fact.Restrictive = true
			return fact, nil
		}
		fact.TrustedOwn = true
		fact.Restrictive = false
		return fact, nil
	}
	fact.TrustedOwn = true
	fact.Restrictive = true
	return fact, nil
}

func ownerRejected(err error) bool {
	return err != nil && strings.Contains(err.Error(), "owned by")
}

func queryServiceFacts() (serviceFacts, error) {
	m, err := mgr.Connect()
	if err != nil {
		return serviceFacts{}, err
	}
	defer m.Disconnect()
	s, err := m.OpenService(runtime.ServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return serviceFacts{}, nil
	}
	if err != nil {
		return serviceFacts{}, err
	}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return serviceFacts{}, err
	}
	return factsFromConfig(cfg), nil
}

func factsFromConfig(cfg mgr.Config) serviceFacts {
	facts := serviceFacts{Exists: true, LocalSystem: localSystemAccount(cfg.ServiceStartName)}
	argv, err := windows.DecomposeCommandLine(cfg.BinaryPathName)
	if err != nil || len(argv) != 3 || argv[1] != "--base-dir" {
		return facts
	}
	facts.DecomposeOK = true
	facts.Binary = argv[0]
	facts.BaseDir = argv[2]
	return facts
}

func machinePilotCount() (int, error) {
	root := os.Getenv("SystemRoot")
	if root == "" {
		return 0, fmt.Errorf("preflight conflict: could not inspect machine launchers")
	}
	n, err := countTaskLaunchers(filepath.Join(root, "System32", "Tasks"))
	if err != nil {
		return 0, err
	}
	runs, err := machineRunCommands()
	if err != nil {
		return 0, err
	}
	return n + countRunLaunchers(runs), nil
}

func machineRunCommands() ([]string, error) {
	var out []string
	for _, sub := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Run`,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\RunOnce`,
	} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, sub, registry.READ|registry.WOW64_64KEY)
		if err != nil {
			if errors.Is(err, registry.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("preflight conflict: could not inspect machine launchers")
		}
		names, err := k.ReadValueNames(-1)
		if err != nil {
			k.Close()
			return nil, fmt.Errorf("preflight conflict: could not inspect machine launchers")
		}
		if len(names) > 256 {
			k.Close()
			return nil, fmt.Errorf("preflight conflict: machine launcher inspection exceeded its bound")
		}
		for _, name := range names {
			val, _, err := k.GetStringValue(name)
			if err != nil {
				continue
			}
			out = append(out, val)
		}
		if err := k.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
