//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type winSCM struct{}

// DefaultSCM opens the local SCM for Type=scm orchestration.
// It never creates, changes, or deletes service configuration.
func DefaultSCM() SCM {
	return winSCM{}
}

func (winSCM) Start(ctx context.Context, name string, timeout time.Duration) (SCMStatus, error) {
	return withService(name, func(s *mgr.Service) (SCMStatus, error) {
		st, err := s.Query()
		if err != nil {
			return SCMStatus{}, fmt.Errorf("query service %s: %w", name, err)
		}
		if st.State == svc.Running {
			return toSCMStatus(st), nil
		}
		if err := s.Start(); err != nil && !isAlreadyRunning(err) {
			st, qerr := s.Query()
			if qerr == nil && st.State == svc.Running {
				return toSCMStatus(st), nil
			}
			return toSCMStatus(st), fmt.Errorf("StartService %s: %w", name, err)
		}
		return waitSCM(ctx, s, name, svc.Running, timeout, true)
	})
}

func (winSCM) Stop(ctx context.Context, name string, timeout time.Duration) (SCMStatus, error) {
	return withService(name, func(s *mgr.Service) (SCMStatus, error) {
		st, err := s.Query()
		if err != nil {
			return SCMStatus{}, fmt.Errorf("query service %s: %w", name, err)
		}
		if st.State == svc.Stopped {
			return toSCMStatus(st), nil
		}
		if _, err := s.Control(svc.Stop); err != nil && !isAlreadyStopped(err) {
			st, qerr := s.Query()
			if qerr == nil && st.State == svc.Stopped {
				return toSCMStatus(st), nil
			}
			return toSCMStatus(st), fmt.Errorf("StopService %s: %w", name, err)
		}
		return waitSCM(ctx, s, name, svc.Stopped, timeout, false)
	})
}

func (winSCM) Query(name string) (SCMStatus, error) {
	return withService(name, func(s *mgr.Service) (SCMStatus, error) {
		st, err := s.Query()
		if err != nil {
			return SCMStatus{}, fmt.Errorf("query service %s: %w", name, err)
		}
		return toSCMStatus(st), nil
	})
}

func withService(name string, fn func(*mgr.Service) (SCMStatus, error)) (SCMStatus, error) {
	if name == "" {
		return SCMStatus{}, fmt.Errorf("ServiceName is empty")
	}
	m, err := mgr.Connect()
	if err != nil {
		return SCMStatus{}, fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return SCMStatus{}, fmt.Errorf("open service %s: %w", name, err)
	}
	defer s.Close()
	return fn(s)
}

func waitSCM(ctx context.Context, s *mgr.Service, name string, want svc.State, timeout time.Duration, starting bool) (SCMStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout = scmWaitTimeout(timeout)
	deadline := time.Now().Add(timeout)
	for {
		st, err := s.Query()
		if err != nil {
			return SCMStatus{}, fmt.Errorf("query service %s: %w", name, err)
		}
		if st.State == want {
			return toSCMStatus(st), nil
		}
		if starting && st.State == svc.Stopped {
			return toSCMStatus(st), fmt.Errorf("service %s stopped while starting", name)
		}
		if err := ctx.Err(); err != nil {
			return toSCMStatus(st), fmt.Errorf("timeout waiting for %s to become %s: %w", name, scmWantName(want), err)
		}
		if time.Now().After(deadline) {
			return toSCMStatus(st), fmt.Errorf("timeout waiting for %s to become %s", name, scmWantName(want))
		}
		sleep := defaultSCMPoll
		if rem := time.Until(deadline); rem > 0 && rem < sleep {
			sleep = rem
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return toSCMStatus(st), fmt.Errorf("timeout waiting for %s to become %s: %w", name, scmWantName(want), ctx.Err())
		case <-timer.C:
		}
	}
}

func toSCMStatus(st svc.Status) SCMStatus {
	return SCMStatus{State: SCMState(st.State), PID: int(st.ProcessId)}
}

func isAlreadyRunning(err error) bool {
	return errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING)
}

func isAlreadyStopped(err error) bool {
	return errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE)
}

func scmWantName(want svc.State) string {
	switch want {
	case svc.Running:
		return "running"
	case svc.Stopped:
		return "stopped"
	default:
		return fmt.Sprintf("state(%d)", int(want))
	}
}

const defaultSCMPoll = 100 * time.Millisecond
