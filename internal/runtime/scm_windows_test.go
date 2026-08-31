//go:build windows

package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestServiceConfig(t *testing.T) {
	cfg := serviceConfig()
	if cfg.DisplayName != DisplayName {
		t.Fatalf("DisplayName = %q", cfg.DisplayName)
	}
	if cfg.StartType != mgr.StartAutomatic {
		t.Fatalf("StartType = %d", cfg.StartType)
	}
	if !cfg.DelayedAutoStart {
		t.Fatal("DelayedAutoStart not set")
	}
	if cfg.ServiceStartName != "" {
		t.Fatalf("ServiceStartName = %q (want empty LocalSystem)", cfg.ServiceStartName)
	}

	actions := recoveryActions()
	if len(actions) != RecoveryActionCount {
		t.Fatalf("recovery actions = %d", len(actions))
	}
	for i, a := range actions {
		if a.Type != mgr.ServiceRestart {
			t.Fatalf("action %d type = %d", i, a.Type)
		}
		if a.Delay != RecoveryDelay {
			t.Fatalf("action %d delay = %s", i, a.Delay)
		}
	}
}

func TestAcceptedControlsIncludePreshutdown(t *testing.T) {
	if acceptedControls&svc.AcceptStop == 0 {
		t.Fatal("missing AcceptStop")
	}
	if acceptedControls&svc.AcceptShutdown == 0 {
		t.Fatal("missing AcceptShutdown")
	}
	if acceptedControls&svc.AcceptPreShutdown == 0 {
		t.Fatal("missing AcceptPreShutdown")
	}
	if acceptedControls&svc.AcceptSessionChange == 0 {
		t.Fatal("missing AcceptSessionChange")
	}
	if !isStopCmd(svc.Stop) || !isStopCmd(svc.Shutdown) || !isStopCmd(svc.PreShutdown) {
		t.Fatal("stop/shutdown/preshutdown must cancel the host")
	}
	if isStopCmd(svc.Pause) || isStopCmd(svc.Interrogate) {
		t.Fatal("pause/interrogate must not be treated as stop")
	}
}

func TestHostStopCommandsRunThenReturn(t *testing.T) {
	for _, cmd := range []svc.Cmd{svc.Stop, svc.Shutdown, svc.PreShutdown} {
		t.Run(cmdName(cmd), func(t *testing.T) {
			var mu sync.Mutex
			ran := false
			h := &host{run: func(ctx context.Context) error {
				<-ctx.Done()
				mu.Lock()
				ran = true
				mu.Unlock()
				return nil
			}}
			reqs := make(chan svc.ChangeRequest, 2)
			changes := make(chan svc.Status, 8)
			done := make(chan struct{})
			var ssec bool
			var errno uint32
			go func() {
				ssec, errno = h.Execute(nil, reqs, changes)
				close(done)
			}()

			deadline := time.Now().Add(3 * time.Second)
			running := false
			for time.Now().Before(deadline) && !running {
				select {
				case st := <-changes:
					if st.State == svc.Running {
						running = true
					}
				case <-time.After(20 * time.Millisecond):
				}
			}
			if !running {
				t.Fatal("host never reported Running")
			}

			reqs <- svc.ChangeRequest{Cmd: cmd}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("Execute did not return after stop command")
			}
			mu.Lock()
			got := ran
			mu.Unlock()
			if !got {
				t.Fatal("stop/shutdown/preshutdown must cancel run so ordered stop can finish")
			}
			if ssec || errno != 0 {
				t.Fatalf("ssec=%v errno=%d", ssec, errno)
			}
		})
	}
}

func cmdName(cmd svc.Cmd) string {
	switch cmd {
	case svc.Stop:
		return "stop"
	case svc.Shutdown:
		return "shutdown"
	case svc.PreShutdown:
		return "preshutdown"
	default:
		return "other"
	}
}
