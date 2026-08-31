//go:build windows

package runtime

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func runSCMProxyTestService() {
	name := scmProxyServiceNameFromArgs()
	if err := svc.Run(name, &scmProxyTestService{}); err != nil {
		fmt.Fprintf(os.Stderr, "scm-proxy service %s: %v\n", name, err)
		os.Exit(1)
	}
}

func scmProxyServiceNameFromArgs() string {
	for _, a := range os.Args[1:] {
		if a == "" || a == winunitdHelperArgPrefix+"scm-proxy" {
			continue
		}
		if len(a) > len(winunitdHelperArgPrefix) && a[:len(winunitdHelperArgPrefix)] == winunitdHelperArgPrefix {
			continue
		}
		return a
	}
	return "winunitd-p7-test"
}

type scmProxyTestService struct{}

func (scmProxyTestService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	time.Sleep(150 * time.Millisecond)
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			changes <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			changes <- svc.Status{State: svc.StopPending}
			return false, 0
		}
	}
	return false, 0
}

func TestSCMProxyThrowawayService(t *testing.T) {
	name := installThrowawaySCM(t)
	scm := DefaultSCM()
	ctx := context.Background()

	st, err := scm.Start(ctx, name, 15*time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if st.ActiveState() != "active" {
		t.Fatalf("after start: %+v", st)
	}
	if st.PID <= 0 {
		t.Fatalf("MainPID missing: %+v", st)
	}

	again, err := scm.Start(ctx, name, 15*time.Second)
	if err != nil {
		t.Fatalf("already-running start: %v", err)
	}
	if again.ActiveState() != "active" {
		t.Fatalf("already-running: %+v", again)
	}

	q, err := scm.Query(name)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if q.ActiveState() != "active" {
		t.Fatalf("query after start: %+v", q)
	}

	stopped, err := scm.Stop(ctx, name, 15*time.Second)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.ActiveState() != "inactive" {
		t.Fatalf("after stop: %+v", stopped)
	}

	againStop, err := scm.Stop(ctx, name, 15*time.Second)
	if err != nil {
		t.Fatalf("already-stopped stop: %v", err)
	}
	if againStop.ActiveState() != "inactive" {
		t.Fatalf("already-stopped: %+v", againStop)
	}
}

func installThrowawaySCM(t *testing.T) string {
	t.Helper()
	m, err := mgr.Connect()
	if err != nil {
		t.Skipf("SCM not available: %v", err)
	}
	defer m.Disconnect()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("wu-p7-%d-%d", os.Getpid(), time.Now().UnixNano()%1e9)
	s, err := m.CreateService(name, exe, mgr.Config{
		DisplayName: name,
		Description: "winunitd P7 throwaway test service; created and deleted by the test",
		StartType:   mgr.StartManual,
	}, winunitdHelperArgPrefix+"scm-proxy", name)
	if err != nil {
		t.Skipf("CreateService %s: %v (need admin to create an isolated test service)", name, err)
	}
	t.Cleanup(func() {
		st, qerr := s.Query()
		if qerr == nil && st.State != svc.Stopped {
			_, _ = s.Control(svc.Stop)
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				st, qerr = s.Query()
				if qerr != nil || st.State == svc.Stopped {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		if err := s.Delete(); err != nil {
			t.Errorf("delete throwaway service %s: %v", name, err)
		}
		_ = s.Close()
	})
	return name
}
