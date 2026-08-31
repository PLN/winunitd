//go:build windows

package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func runManagerSCMProxyTestService() {
	name := managerSCMProxyServiceName()
	if err := svc.Run(name, &managerSCMProxyService{}); err != nil {
		fmt.Fprintf(os.Stderr, "scm-proxy service %s: %v\n", name, err)
		os.Exit(1)
	}
}

func managerSCMProxyServiceName() string {
	for _, a := range os.Args[1:] {
		if a == "" || strings.HasPrefix(a, winunitdHelperArgPrefix) {
			continue
		}
		return a
	}
	return "winunitd-p7-test"
}

type managerSCMProxyService struct{}

func (managerSCMProxyService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
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

func TestWindowsSCMProxyOrchestration(t *testing.T) {
	svcName := installManagerThrowawaySCM(t)
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "mssql.service", fmt.Sprintf(`
[Service]
Type=scm
ServiceName=%s
TimeoutStartSec=15s
TimeoutStopSec=15s
`, svcName))
	writeUnit(t, units, "app.service", fmt.Sprintf(`
[Unit]
Requires=mssql.service
After=mssql.service
[Service]
Type=simple
ExecStart=%s
ExecStartArg=%ssleep
WorkingDirectory=C:\
`, exe, winunitdHelperArgPrefix))

	m, err := New(Config{BaseDir: dir, SCM: runtime.DefaultSCM()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}

	st, err := m.Start(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveState != "active" {
		t.Fatalf("app start = %+v", st)
	}

	proxy, err := m.Status("mssql")
	if err != nil {
		t.Fatal(err)
	}
	if proxy.Unit == nil || proxy.Unit.ActiveState != "active" {
		t.Fatalf("proxy status after After= start = %+v", proxy.Unit)
	}
	if proxy.Unit.MainPID <= 0 {
		t.Fatalf("SCM MainPID missing: %+v", proxy.Unit)
	}
	if proxy.Unit.InvocationID != "" {
		t.Fatalf("Type=scm must omit InvocationID: %+v", proxy.Unit)
	}

	live, err := runtime.DefaultSCM().Query(svcName)
	if err != nil {
		t.Fatal(err)
	}
	if live.ActiveState() != "active" {
		t.Fatalf("SCM after After= wait = %+v (simple unit must wait until running)", live)
	}

	again, err := m.Start(context.Background(), "mssql")
	if err != nil {
		t.Fatal(err)
	}
	if again.ActiveState != "active" {
		t.Fatalf("already-running start = %+v", again)
	}

	stopped, err := m.Stop("mssql")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.ActiveState != "inactive" {
		t.Fatalf("stop = %+v", stopped)
	}
	afterStop, err := m.Status("mssql")
	if err != nil {
		t.Fatal(err)
	}
	if afterStop.Unit.ActiveState != "inactive" {
		t.Fatalf("status after stop = %+v", afterStop.Unit)
	}

	againStop, err := m.Stop("mssql")
	if err != nil {
		t.Fatal(err)
	}
	if againStop.ActiveState != "inactive" {
		t.Fatalf("already-stopped = %+v", againStop)
	}
}

func installManagerThrowawaySCM(t *testing.T) string {
	t.Helper()
	cm, err := mgr.Connect()
	if err != nil {
		t.Skipf("SCM not available: %v", err)
	}
	defer cm.Disconnect()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("wu-p7-%d-%d", os.Getpid(), time.Now().UnixNano()%1e9)
	s, err := cm.CreateService(name, exe, mgr.Config{
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
