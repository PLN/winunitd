//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// This experiment has one fixed service and one MSI transaction at a time.
// Production servicing needs transaction-scoped state and broader path checks.
type savedService struct {
	Exists      bool
	Running     bool
	Config      mgr.Config
	Actions     []mgr.RecoveryAction
	Reset       uint32
	NonCrash    bool
	Preshutdown uint32
}

func maintenance(operation, token string) error {
	if operation != "configure" && !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(token) {
		return fmt.Errorf("fixture transaction token required")
	}
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFilesX64, 0)
	if err != nil {
		return err
	}
	// Program Files inheritance restricts mutation to administrators/SYSTEM.
	statePath := filepath.Join(base, "winunitd-msi-fixture-rollback-"+token+".json")
	if operation == "commit" {
		return removeState(statePath)
	}
	if operation != "prepare" && operation != "rollback" && operation != "configure" {
		return fmt.Errorf("unknown fixed fixture operation")
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if operation == "rollback" {
		data, err := os.ReadFile(statePath)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var saved savedService
		if err := json.Unmarshal(data, &saved); err != nil {
			return err
		}
		if saved.Exists {
			s, err := m.OpenService(serviceName)
			if err != nil {
				return err
			}
			defer s.Close()
			if err := restore(s, saved); err != nil {
				return err
			}
			st, err := s.Query()
			if err != nil {
				return err
			}
			if saved.Running && st.State == svc.StopPending {
				if err := waitState(s, svc.Stopped, time.Now().Add(30*time.Second)); err != nil {
					return err
				}
				st.State = svc.Stopped
			}
			if saved.Running && st.State == svc.Stopped {
				if err := s.Start(); err != nil {
					return err
				}
				if err := waitState(s, svc.Running, time.Now().Add(30*time.Second)); err != nil {
					return err
				}
			} else if !saved.Running && st.State != svc.Stopped {
				if err := stop(s); err != nil {
					return err
				}
			}
			st, err = s.Query()
			if err != nil {
				return err
			}
			if saved.Running != (st.State == svc.Running) {
				return fmt.Errorf("rollback did not restore fixture running state")
			}
		}
		return removeState(statePath)
	}
	s, err := m.OpenService(serviceName)
	if err != nil && !(operation == "prepare" && errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST)) {
		return err
	}
	var saved savedService
	if s != nil {
		defer s.Close()
		if operation == "configure" {
			cfg, err := s.Config()
			if err != nil {
				return err
			}
			cfg.DelayedAutoStart = true
			a := mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: time.Second}
			return restore(s, savedService{Config: cfg, Actions: []mgr.RecoveryAction{a, a, a}, Reset: windows.INFINITE, NonCrash: true, Preshutdown: 180000})
		}
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State != svc.Running && st.State != svc.Stopped {
			return fmt.Errorf("fixture is in transition")
		}
		saved.Exists, saved.Running = true, st.State == svc.Running
		if saved.Config, err = s.Config(); err != nil {
			return err
		}
		if saved.Config.ServiceStartName != "LocalSystem" {
			return fmt.Errorf("fixture identity conflict")
		}
		if saved.Actions, err = s.RecoveryActions(); err != nil {
			return err
		}
		if saved.Reset, err = s.ResetPeriod(); err != nil {
			return err
		}
		if saved.NonCrash, err = s.RecoveryActionsOnNonCrashFailures(); err != nil {
			return err
		}
		var needed uint32
		if err := windows.QueryServiceConfig2(s.Handle, windows.SERVICE_CONFIG_PRESHUTDOWN_INFO, (*byte)(unsafe.Pointer(&saved.Preshutdown)), 4, &needed); err != nil {
			return err
		}
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(statePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create rollback state (stale state requires inspection): %w", err)
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if saved.Running {
		return stop(s)
	}
	return nil
}

func restore(s *mgr.Service, saved savedService) error {
	if err := s.UpdateConfig(saved.Config); err != nil {
		return err
	}
	if len(saved.Actions) == 0 {
		if err := s.ResetRecoveryActions(); err != nil {
			return err
		}
	} else if err := s.SetRecoveryActions(saved.Actions, saved.Reset); err != nil {
		return err
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(saved.NonCrash); err != nil {
		return err
	}
	return windows.ChangeServiceConfig2(s.Handle, windows.SERVICE_CONFIG_PRESHUTDOWN_INFO, (*byte)(unsafe.Pointer(&saved.Preshutdown)))
}

func stop(s *mgr.Service) error {
	deadline := time.Now().Add(180 * time.Second)
	st, err := s.Query()
	if err != nil {
		return err
	}
	var process windows.Handle
	if st.ProcessId != 0 {
		process, err = windows.OpenProcess(windows.SYNCHRONIZE, false, st.ProcessId)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(process)
	}
	if st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil {
			return err
		}
	}
	if err := waitState(s, svc.Stopped, deadline); err != nil {
		return err
	}
	if process != 0 {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("fixture process deadline expired")
		}
		result, err := windows.WaitForSingleObject(process, uint32(remaining/time.Millisecond))
		if err != nil {
			return err
		}
		if result != windows.WAIT_OBJECT_0 {
			return fmt.Errorf("fixture process termination unconfirmed: %d", result)
		}
	}
	return nil
}

func waitState(s *mgr.Service, want svc.State, deadline time.Time) error {
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("fixture state deadline expired")
}

func removeState(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
