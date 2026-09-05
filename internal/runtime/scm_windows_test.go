//go:build windows

package runtime

import (
	"context"
	"errors"
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
	if acceptedControls&svc.AcceptPowerEvent == 0 {
		t.Fatal("missing AcceptPowerEvent")
	}
	if acceptedControls&svc.Accepted(ServiceAcceptTimeChange) == 0 {
		t.Fatal("missing SERVICE_ACCEPT_TIMECHANGE")
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

func TestHostClockChangeCommandsCallOnClock(t *testing.T) {
	cases := []struct {
		name      string
		cmd       svc.Cmd
		eventType uint32
		want      bool
	}{
		{name: "timechange", cmd: svc.Cmd(ServiceControlTimeChange), want: true},
		{name: "resume-automatic", cmd: svc.PowerEvent, eventType: PBTAPMResumeAutomatic, want: true},
		{name: "resume-suspend", cmd: svc.PowerEvent, eventType: PBTAPMResumeSuspend, want: true},
		{name: "power-suspend", cmd: svc.PowerEvent, eventType: 0x0004, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			n := 0
			h := &host{
				run: func(ctx context.Context) error {
					<-ctx.Done()
					return nil
				},
				onClock: func() {
					mu.Lock()
					n++
					mu.Unlock()
				},
			}
			reqs := make(chan svc.ChangeRequest, 2)
			changes := make(chan svc.Status, 8)
			done := make(chan struct{})
			go func() {
				h.Execute(nil, reqs, changes)
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

			reqs <- svc.ChangeRequest{Cmd: tc.cmd, EventType: tc.eventType}
			deadline = time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				mu.Lock()
				got := n
				mu.Unlock()
				if got > 0 {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			mu.Lock()
			got := n
			mu.Unlock()
			if tc.want && got != 1 {
				t.Fatalf("onClock = %d, want 1", got)
			}
			if !tc.want && got != 0 {
				t.Fatalf("onClock = %d, want 0", got)
			}

			reqs <- svc.ChangeRequest{Cmd: svc.Stop}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("Execute did not return after stop")
			}
		})
	}
}

func TestHostStopPendingWaitHintAndCheckPoint(t *testing.T) {
	orig := stopPendingTick
	stopPendingTick = 15 * time.Millisecond
	t.Cleanup(func() { stopPendingTick = orig })

	h := &host{run: func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(60 * time.Millisecond)
		return nil
	}}
	reqs := make(chan svc.ChangeRequest, 2)
	changes := make(chan svc.Status, 64)
	done := make(chan struct{})
	go func() {
		h.Execute(nil, reqs, changes)
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

	reqs <- svc.ChangeRequest{Cmd: svc.Stop}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not return after stop")
	}

	wantHint := uint32(StopPendingWaitHint / time.Millisecond)
	var pending []svc.Status
	drain := true
	for drain {
		select {
		case st := <-changes:
			if st.State == svc.StopPending {
				pending = append(pending, st)
			}
		default:
			drain = false
		}
	}
	if len(pending) == 0 {
		t.Fatal("StopPending was not reported")
	}
	if pending[0].WaitHint != wantHint {
		t.Fatalf("WaitHint = %d, want %d", pending[0].WaitHint, wantHint)
	}
	if pending[0].CheckPoint == 0 {
		t.Fatal("first StopPending must set CheckPoint")
	}
	sawBump := false
	for _, st := range pending[1:] {
		if st.WaitHint != wantHint {
			t.Fatalf("WaitHint = %d, want %d", st.WaitHint, wantHint)
		}
		if st.CheckPoint > pending[0].CheckPoint {
			sawBump = true
		}
	}
	if !sawBump {
		t.Fatal("CheckPoint must increment during ordered stop")
	}
}

func TestHostPreservesFailureJoinedWithCancellation(t *testing.T) {
	for _, failed := range []bool{false, true} {
		h := &host{run: func(ctx context.Context) error {
			<-ctx.Done()
			if failed {
				return errors.Join(ctx.Err(), errors.New("injected shutdown failure"))
			}
			return ctx.Err()
		}}
		requests := make(chan svc.ChangeRequest, 1)
		requests <- svc.ChangeRequest{Cmd: svc.Stop}
		changes := make(chan svc.Status, 64)
		type result struct {
			specific bool
			code     uint32
		}
		done := make(chan result, 1)
		go func() { specific, code := h.Execute(nil, requests, changes); done <- result{specific, code} }()
		select {
		case got := <-done:
			if got.specific != failed || (got.code != 0) != failed {
				t.Fatalf("failed=%v result=%+v", failed, got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("SCM shutdown did not return")
		}
	}
}
