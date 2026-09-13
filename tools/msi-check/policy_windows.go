//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"unsafe"

	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

var policyToken = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Only policy fields are restored; MSI owns service registration and payloads.
type savedPolicy struct {
	Version     int
	Transaction string
	Exists      bool
	StartType   uint32
	Delayed     bool
	Actions     []mgr.RecoveryAction
	Reset       uint32
	NonCrash    bool
	Preshutdown uint32
	Command     string
	RebootText  string
}

func policy(args []string) error {
	if len(args) != 4 || !policyToken.MatchString(args[3]) {
		return fmt.Errorf("invalid policy transaction arguments")
	}
	op, token := args[0], args[3]
	if op != "policy-prepare" && op != "policy-apply" && op != "policy-rollback" && op != "policy-commit" {
		return fmt.Errorf("unknown policy operation")
	}
	// Reuse exact path, ACL, service identity and stopped-servicing checks.
	// Commit follows successful StartServices and only retires saved state.
	if err := checkRegistration([]string{"check", args[1], args[2], "policy"}, op != "policy-commit"); err != nil {
		return err
	}
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX64, 0)
	if err != nil {
		return err
	}
	// Standard Program Files protects the parent from non-administrator writes.
	// The product-path check above rejects redirected ancestors. Each state file
	// additionally has an explicit SYSTEM/Administrators-only descriptor.
	statePath := filepath.Join(base, "winunitd-policy-"+token+".json")
	if op == "policy-rollback" || op == "policy-commit" {
		saved, err := readPolicy(statePath, token)
		if errors.Is(err, os.ErrNotExist) {
			return nil // Prepare may not have run before rollback.
		}
		if err != nil {
			return err
		}
		if op == "policy-rollback" && saved.Exists {
			m, err := mgr.Connect()
			if err != nil {
				return err
			}
			defer m.Disconnect()
			s, err := m.OpenService(runtime.ServiceName)
			if err != nil {
				return err
			}
			defer s.Close()
			if err := restorePolicy(s, saved); err != nil {
				return err
			}
		}
		return os.Remove(statePath)
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(runtime.ServiceName)
	if err != nil && !(op == "policy-prepare" && errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST)) {
		return err
	}
	if s != nil {
		defer s.Close()
	}
	if op == "policy-apply" {
		if _, err := readPolicy(statePath, token); err != nil {
			return fmt.Errorf("policy apply requires saved transaction: %w", err)
		}
		return runtime.ApplyServicePolicy(s)
	}
	saved := savedPolicy{Version: 1, Transaction: token}
	if s != nil {
		if err := capturePolicy(s, &saved); err != nil {
			return err
		}
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if len(data) > 64*1024 || len(saved.Actions) > 64 {
		return fmt.Errorf("existing recovery policy exceeds rollback bounds")
	}
	f, err := createPolicyState(statePath)
	if err != nil {
		return fmt.Errorf("create exclusive policy state: %w", err)
	}
	_, writeErr := f.Write(data)
	return errors.Join(writeErr, f.Sync(), f.Close())
}

func createPolicyState(path string) (*os.File, error) {
	sd, err := windows.SecurityDescriptorFromString("O:BAG:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func readPolicy(path, token string) (savedPolicy, error) {
	var saved savedPolicy
	info, err := os.Lstat(path)
	if err != nil {
		return saved, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return saved, fmt.Errorf("invalid policy state file")
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return saved, err
	}
	if err := protectedSecurity(sd); err != nil {
		return saved, err
	}
	f, err := os.Open(path)
	if err != nil {
		return saved, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err := errors.Join(readErr, f.Close()); err != nil {
		return saved, err
	}
	if len(data) > 64*1024 {
		return saved, fmt.Errorf("policy state exceeds limit")
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return saved, err
	}
	if saved.Version != 1 || saved.Transaction != token || len(saved.Actions) > 64 {
		return saved, fmt.Errorf("policy state identity or version mismatch")
	}
	return saved, nil
}

func capturePolicy(s *mgr.Service, saved *savedPolicy) error {
	cfg, err := s.Config()
	if err != nil {
		return err
	}
	saved.Exists, saved.StartType, saved.Delayed = true, cfg.StartType, cfg.DelayedAutoStart
	if saved.Actions, err = s.RecoveryActions(); err != nil {
		return err
	}
	if saved.Reset, err = s.ResetPeriod(); err != nil {
		return err
	}
	if saved.NonCrash, err = s.RecoveryActionsOnNonCrashFailures(); err != nil {
		return err
	}
	if saved.Command, err = s.RecoveryCommand(); err != nil {
		return err
	}
	if saved.RebootText, err = s.RebootMessage(); err != nil {
		return err
	}
	var needed uint32
	return windows.QueryServiceConfig2(s.Handle, windows.SERVICE_CONFIG_PRESHUTDOWN_INFO, (*byte)(unsafe.Pointer(&saved.Preshutdown)), 4, &needed)
}

func restorePolicy(s *mgr.Service, saved savedPolicy) error {
	cfg, err := s.Config()
	if err != nil {
		return err
	}
	cfg.StartType, cfg.DelayedAutoStart = saved.StartType, saved.Delayed
	if err := s.UpdateConfig(cfg); err != nil {
		return err
	}
	if len(saved.Actions) == 0 {
		err = s.ResetRecoveryActions()
	} else {
		err = s.SetRecoveryActions(saved.Actions, saved.Reset)
	}
	if err != nil {
		return err
	}
	if err := s.SetRecoveryCommand(saved.Command); err != nil {
		return err
	}
	if err := s.SetRebootMessage(saved.RebootText); err != nil {
		return err
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(saved.NonCrash); err != nil {
		return err
	}
	return windows.ChangeServiceConfig2(s.Handle, windows.SERVICE_CONFIG_PRESHUTDOWN_INFO, (*byte)(unsafe.Pointer(&saved.Preshutdown)))
}
