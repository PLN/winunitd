//go:build windows

package runtime

import (
	"testing"

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
	if !isStopCmd(svc.Stop) || !isStopCmd(svc.Shutdown) || !isStopCmd(svc.PreShutdown) {
		t.Fatal("stop/shutdown/preshutdown must cancel the host")
	}
	if isStopCmd(svc.Pause) || isStopCmd(svc.Interrogate) {
		t.Fatal("pause/interrogate must not be treated as stop")
	}
}
