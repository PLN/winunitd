package manager

import (
	"context"
	"testing"

	"github.com/PLN/winunitd/internal/core"
)

func TestStopPlansRetainInvocationMembershipAndOrdering(t *testing.T) {
	for _, action := range []string{"stop", "restart", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			launch := &fakeLauncher{}
			worker := "[Service]\nExecStart=C:\\Tools\\worker.exe\n"
			m := managerWith(t, launch, map[string]string{
				"app.target":   "[Unit]\nDescription=Original group\n",
				"other.target": "[Unit]\nDescription=Replacement group\n",
				"web.service":  "[Unit]\nPartOf=app.target\nAfter=db.service\n" + worker,
				"db.service":   "[Unit]\nPartOf=app.target\n" + worker,
			})
			for _, name := range []string{"app.target", "db.service", "web.service"} {
				if _, err := m.Start(context.Background(), name); err != nil {
					t.Fatal(err)
				}
			}
			// The new revision both retargets participation and reverses ordering.
			// Existing invocations must still stop web before db in the original group.
			writeUnit(t, m.cfg.UnitsDir(), "web.service", "[Unit]\nPartOf=other.target\n"+worker)
			writeUnit(t, m.cfg.UnitsDir(), "db.service", "[Unit]\nPartOf=other.target\nAfter=web.service\n"+worker)
			if _, err := m.Reload(); err != nil {
				t.Fatal(err)
			}
			var err error
			switch action {
			case "stop":
				_, err = m.Stop("app.target")
			case "restart":
				_, err = m.Restart(context.Background(), "app.target")
			case "shutdown":
				err = m.Shutdown(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			got := launch.stopped()
			if len(got) != 2 || got[0] != "web.service" || got[1] != "db.service" {
				t.Fatalf("captured stop order: %v", got)
			}
			want := core.Inactive
			if action == "restart" {
				want = core.Active
			}
			for _, name := range []string{"web.service", "db.service"} {
				assertState(t, m, name, want)
			}
			if action == "restart" {
				if _, err := m.Stop("app.target"); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"web.service", "db.service"} {
					assertState(t, m, name, core.Active)
				}
				if _, err := m.Stop("other.target"); err != nil {
					t.Fatal(err)
				}
				got = launch.stopped()
				if len(got) != 4 || got[2] != "db.service" || got[3] != "web.service" {
					t.Fatalf("new invocation did not use new policy: %v", got)
				}
			}
		})
	}
}
