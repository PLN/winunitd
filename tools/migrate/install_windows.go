//go:build windows

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func executeMSISupported(bool) error { return nil }

func executeMSI(path string) error {
	cmd := exec.Command("msiexec.exe", "/i", path, "/qn", "/norestart")
	err := cmd.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 3010 {
			return nil
		}
		return fmt.Errorf("msiexec exit %d", exitErr.ExitCode())
	}
	return fmt.Errorf("msiexec failed")
}

func stopInstalledService(executed bool) error {
	if !executed {
		return nil
	}
	return serviceControl("stop", map[int]bool{0: true, 1062: true})
}

func startInstalledService(want bool) error {
	if !want {
		return nil
	}
	return serviceControl("start", map[int]bool{0: true, 1056: true})
}

func serviceControl(op string, ok map[int]bool) error {
	cmd := exec.Command("sc.exe", op, serviceName)
	err := cmd.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && ok[exitErr.ExitCode()] {
		return nil
	}
	if errors.As(err, &exitErr) {
		return fmt.Errorf("sc %s winunitd exit %d", op, exitErr.ExitCode())
	}
	return fmt.Errorf("sc %s winunitd failed", op)
}

func productServiceMatches(check bool) error {
	if !check {
		return nil
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service identity")
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service identity")
	}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil || cfg.ServiceStartName != "LocalSystem" {
		return fmt.Errorf("service identity")
	}
	argv, err := windows.DecomposeCommandLine(cfg.BinaryPathName)
	if err != nil || len(argv) != 3 || argv[1] != "--base-dir" {
		return fmt.Errorf("service identity")
	}
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX64, 0)
	if err != nil {
		return fmt.Errorf("service identity")
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return fmt.Errorf("service identity")
	}
	wantExe := filepath.Join(programFiles, "winunitd", "bin", "winunitd.exe")
	wantData := filepath.Join(programData, "winunitd")
	if !strings.EqualFold(filepath.Clean(argv[0]), filepath.Clean(wantExe)) {
		return fmt.Errorf("service identity")
	}
	if !strings.EqualFold(filepath.Clean(argv[2]), filepath.Clean(wantData)) {
		return fmt.Errorf("service identity")
	}
	st, err := s.Query()
	if err != nil || st.State != svc.Running {
		return fmt.Errorf("service identity")
	}
	return nil
}
