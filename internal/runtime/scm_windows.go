//go:build windows

package runtime

import (
	"context"
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// RunningAsService reports whether this process was started by SCM.
func RunningAsService() (bool, error) {
	return svc.IsWindowsService()
}

func serviceConfig() mgr.Config {
	return mgr.Config{
		DisplayName:      DisplayName,
		Description:      "Declarative Windows unit manager and process supervisor.",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
		// ServiceStartName empty → LocalSystem (DESIGN.md §49).
	}
}

func recoveryActions() []mgr.RecoveryAction {
	a := mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: RecoveryDelay}
	out := make([]mgr.RecoveryAction, RecoveryActionCount)
	for i := range out {
		out[i] = a
	}
	return out
}

func imagePath(exe, baseDir string) string {
	return syscall.EscapeArg(exe) + " " + syscall.EscapeArg("--base-dir") + " " + syscall.EscapeArg(baseDir)
}

func setPreshutdownTimeout(s *mgr.Service, d time.Duration) error {
	info := struct{ PreshutdownTimeout uint32 }{
		PreshutdownTimeout: uint32(d / time.Millisecond),
	}
	return windows.ChangeServiceConfig2(s.Handle, windows.SERVICE_CONFIG_PRESHUTDOWN_INFO, (*byte)(unsafe.Pointer(&info)))
}

func configureService(s *mgr.Service) error {
	if err := s.SetRecoveryActions(recoveryActions(), RecoveryResetPeriodNever); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("set recovery on non-crash failures: %w", err)
	}
	if err := setPreshutdownTimeout(s, PreshutdownTimeout); err != nil {
		return fmt.Errorf("set preshutdown timeout: %w", err)
	}
	return nil
}

// Install registers winunitd with SCM: Automatic (Delayed Start), LocalSystem,
// restart on failure, and preshutdown notification (DESIGN.md §42, §49, §67).
func Install(exePath, baseDir string) error {
	if exePath == "" {
		return fmt.Errorf("executable path required")
	}
	if baseDir == "" {
		return fmt.Errorf("base directory required")
	}
	if err := EnsureDataDirs(baseDir); err != nil {
		return err
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	cfg := serviceConfig()
	created := false
	s, err := m.OpenService(ServiceName)
	if err == nil {
		cfg.BinaryPathName = imagePath(exePath, baseDir)
		if err := s.UpdateConfig(cfg); err != nil {
			s.Close()
			return fmt.Errorf("update service: %w", err)
		}
	} else {
		s, err = m.CreateService(ServiceName, exePath, cfg, "--base-dir", baseDir)
		if err != nil {
			return fmt.Errorf("create service: %w", err)
		}
		created = true
	}
	defer s.Close()

	if err := configureService(s); err != nil {
		if created {
			_ = s.Delete()
		}
		return err
	}

	st, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service: %w", err)
	}
	if st.State == svc.Stopped {
		if err := s.Start(); err != nil {
			return fmt.Errorf("start service: %w", err)
		}
	}
	return nil
}

// Uninstall requests an ordered stop (if running) and removes the winunitd
// service. PATH and Event Log provider are not touched.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("service %s is not installed", ServiceName)
	}
	defer s.Close()
	if err := stopService(s); err != nil {
		return err
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	return nil
}

func stopService(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service: %w", err)
	}
	if st.State == svc.Stopped {
		return nil
	}
	if _, err := s.Control(svc.Stop); err != nil {
		st, qerr := s.Query()
		if qerr == nil && st.State == svc.Stopped {
			return nil
		}
		return fmt.Errorf("stop service: %w", err)
	}
	deadline := time.Now().Add(PreshutdownTimeout)
	for time.Now().Before(deadline) {
		st, err = s.Query()
		if err != nil {
			return fmt.Errorf("query service: %w", err)
		}
		if st.State == svc.Stopped {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("service %s did not stop within %s", ServiceName, PreshutdownTimeout)
}

const acceptedControls = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPreShutdown | svc.AcceptSessionChange | svc.AcceptPowerEvent | svc.Accepted(ServiceAcceptTimeChange)

func isStopCmd(cmd svc.Cmd) bool {
	switch cmd {
	case svc.Stop, svc.Shutdown, svc.PreShutdown:
		return true
	default:
		return false
	}
}

type host struct {
	run       func(ctx context.Context) error
	onSession func(SessionChange)
	onClock   func()
}

func (h *host) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errc := make(chan error, 1)
	go func() {
		errc <- h.run(ctx)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: acceptedControls}
	go overlayTimeChangeAccept(svc.Status{State: svc.Running, Accepts: acceptedControls})

	for {
		select {
		case err := <-errc:
			if err != nil && !IsCancellation(err) {
				return true, 1
			}
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
				go overlayTimeChangeAccept(c.CurrentStatus)
			case svc.SessionChange:
				// Convert the SCM EVENTDATA uintptr here, in the
				// handler, not later. unsafe.Add is the same-expression
				// pointer form; unsafe.Pointer(c.EventData) is a delayed
				// uintptr round-trip (vet / checkptr).
				if sc, ok := ParseSessionChange(c.Cmd, c.EventType, unsafe.Add(unsafe.Pointer(nil), c.EventData)); ok && h.onSession != nil {
					h.onSession(sc)
				}
			default:
				if IsClockChangeControl(uint32(c.Cmd), c.EventType) {
					if h.onClock != nil {
						h.onClock()
					}
					continue
				}
				if isStopCmd(c.Cmd) {
					// Cancel the host. serve() then runs ordered unit stop
					// and closes the daemon Job Object (DESIGN.md §42).
					hint := uint32(StopPendingWaitHint / time.Millisecond)
					checkpoint := uint32(1)
					changes <- svc.Status{State: svc.StopPending, WaitHint: hint, CheckPoint: checkpoint}
					cancel()
					tick := stopPendingTick
					if tick <= 0 {
						tick = time.Second
					}
					ticker := time.NewTicker(tick)
					defer ticker.Stop()
					for {
						select {
						case err := <-errc:
							if err != nil && !IsCancellation(err) {
								return true, 1
							}
							return false, 0
						case <-ticker.C:
							checkpoint++
							changes <- svc.Status{State: svc.StopPending, WaitHint: hint, CheckPoint: checkpoint}
						}
					}
				}
			}
		}
	}
}

// RunHost runs run as the winunitd Windows Service. ctx is cancelled on
// SERVICE_CONTROL_STOP, SHUTDOWN, and PRESHUTDOWN.
func RunHost(run func(ctx context.Context) error) error {
	return RunHostNotify(run, nil, nil)
}

// RunHostNotify is RunHost plus SESSIONCHANGE callbacks (user manager
// auto-start on first logon) and TIMECHANGE / POWEREVENT (resume)
// callbacks so calendar timers recompute (DESIGN.md §18).
func RunHostNotify(run func(ctx context.Context) error, onSession func(SessionChange), onClock func()) error {
	if run == nil {
		return fmt.Errorf("nil service run function")
	}
	return svc.Run(ServiceName, &host{run: run, onSession: onSession, onClock: onClock})
}

func overlayTimeChangeAccept(st svc.Status) {
	// golang.org/x/sys/windows/svc updateStatus does not map
	// SERVICE_ACCEPT_TIMECHANGE (0x200). Re-apply it on the status
	// handle after each SetServiceStatus so SCM will deliver TIMECHANGE.
	h := svc.StatusHandle()
	if h == 0 {
		return
	}
	time.Sleep(5 * time.Millisecond)
	var ss windows.SERVICE_STATUS
	ss.ServiceType = windows.SERVICE_WIN32_OWN_PROCESS
	ss.CurrentState = uint32(st.State)
	ss.ControlsAccepted = uint32(st.Accepts) | ServiceAcceptTimeChange
	ss.CheckPoint = st.CheckPoint
	ss.WaitHint = st.WaitHint
	_ = windows.SetServiceStatus(h, &ss)
}
