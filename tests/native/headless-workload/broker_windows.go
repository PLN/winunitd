//go:build windows

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// brokerStopped requires the installed winunitd service, when registered,
// to be stopped with no process.
func brokerStopped() error {
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
		return errors.New("stop the winunitd service before staging a grant: a running broker would launch the account at once")
	}
	return nil
}
