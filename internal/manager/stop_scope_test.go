package manager

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
)

func TestAcceptedStopScopeSuppressesRecoveryBeforeOrderedTeardown(t *testing.T) {
	for _, action := range []string{"stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			launch := &phasedOperationLauncher{entered: make(chan string, 8), release: map[string]chan struct{}{
				"a.service": make(chan struct{}), "b.service": make(chan struct{}),
			}}
			close(launch.release["b.service"])
			m := managerWith(t, launch, map[string]string{
				"group.target": "[Unit]\nWants=a.service b.service\n",
				"a.service":    "[Unit]\nPartOf=group.target\nAfter=b.service\n[Service]\nExecStart=C:\\Tools\\a.exe\n",
				"b.service":    "[Unit]\nPartOf=group.target\n[Service]\nExecStart=C:\\Tools\\b.exe\nRestart=always\n",
			})
			var once sync.Once
			release := func() { once.Do(func() { close(launch.release["a.service"]) }) }
			t.Cleanup(release)
			if _, err := m.Start(context.Background(), "group.target"); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			rt := m.units["b.service"]
			owner := runtimeIdentity{name: "b.service", record: rt, gen: rt.gen}
			m.mu.Unlock()
			done := make(chan error, 1)
			go func() {
				if action == "restart" {
					_, err := m.Restart(context.Background(), "group.target")
					done <- err
				} else {
					_, err := m.Stop("group.target")
					done <- err
				}
			}()
			select {
			case name := <-launch.entered:
				if name != "a.service" {
					t.Fatalf("first teardown = %s", name)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("first teardown did not enter")
			}
			// b has not reached its stop worker. Recovery must already be
			// suppressed by acceptance of the complete ordered scope.
			m.beginRestart(recoveryRequest{owner: owner})
			m.mu.Lock()
			unexpectedRecovery := rt.sub == core.SubAutoRestart || rt.restartCancel != nil
			m.mu.Unlock()
			if unexpectedRecovery {
				t.Fatal("ordered member admitted recovery after stop scope acceptance")
			}
			release()
			if err := waitErr(t, done); err != nil {
				t.Fatal(err)
			}
			want := core.Inactive
			if action == "restart" {
				want = core.Active
			}
			assertState(t, m, "a.service", want)
			assertState(t, m, "b.service", want)
		})
	}
}
