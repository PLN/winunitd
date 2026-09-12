package manager

import (
	"context"
	"errors"
	"testing"

	"github.com/PLN/winunitd/internal/eventlog"
	"github.com/PLN/winunitd/internal/pathwatch"
	"github.com/PLN/winunitd/internal/registry"
)

func TestWatchAndErrorTransfersCleanupOwnership(t *testing.T) {
	for _, kind := range []string{"path", "exists", "registry", "eventlog"} {
		t.Run(kind, func(t *testing.T) {
			name, definition := "partial.path", "[Path]\nPathChanged=C:\\Data\\fixture\n"
			switch kind {
			case "exists":
				definition = "[Path]\nPathExists=C:\\Data\\fixture\n"
			case "registry":
				name, definition = "partial.registry", "[Registry]\nRegistryChanged=HKLM\\Software\\Example\n"
			case "eventlog":
				name, definition = "partial.eventlog", "[EventLog]\nEventLogTrigger=Application:EventID=1234\n"
			}
			m := managerWith(t, &fakeLauncher{}, map[string]string{name: definition, "partial.service": "[Service]\nExecStart=C:\\Tools\\worker.exe\n"})
			watch := &controlledCloseWatch{}
			watch.fail.Store(true)
			failure := errors.New("open failed after allocation")
			opens := 0
			m.pathOpen = func(pathwatch.Spec) (pathwatch.Watch, error) { opens++; return watch, failure }
			m.pathExistsOpen = m.pathOpen
			m.regOpen = func(registry.Key) (registry.Watch, error) { opens++; return watch, failure }
			m.evtOpen = func(eventlog.Trigger) (eventlog.Subscription, error) { opens++; return watch, failure }
			if _, err := m.Start(context.Background(), name); err == nil {
				t.Fatal("partial open succeeded")
			}
			m.mu.Lock()
			rt := m.units[name]
			retained := rt != nil && rt.hub != nil && rt.cleanupPending()
			m.mu.Unlock()
			if !retained {
				t.Fatal("watch returned with error was discarded")
			}
			if _, err := m.Start(context.Background(), name); err == nil {
				t.Fatal("replacement admitted during failed cleanup")
			}
			if opens != 1 {
				t.Fatal("replacement allocated another watch")
			}
			watch.fail.Store(false)
			if _, err := m.stopUnit(name); err != nil {
				t.Fatal(err)
			}
			if watch.calls.Load() != 2 {
				t.Fatal("stop did not retry the retained watch")
			}
		})
	}
}
