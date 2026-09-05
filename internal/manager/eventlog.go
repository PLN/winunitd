package manager

import (
	"context"
	"errors"
	"fmt"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/eventlog"
	"github.com/PLN/winunitd/internal/unit"
)

func (m *Manager) armEventLog(u *unit.Unit) error {
	if m == nil || u == nil || u.EventLog == nil {
		return nil
	}
	if len(u.EventLog.Triggers) == 0 {
		return fmt.Errorf("%s: EventLogTrigger is required", core.ReasonConfiguration)
	}
	if err := m.disarmHub(u.Name); err != nil {
		return err
	}

	open := m.evtOpen
	if open == nil {
		open = eventlog.OpenSubscribe
	}
	ctx, cancel := context.WithCancel(context.Background())
	var opened []eventlog.Subscription
	for _, tr := range u.EventLog.Triggers {
		s, err := open(tr)
		if err != nil {
			cancel()
			cleanupErr := m.disposeHub(u.Name, &watchRuntime{watches: toWatchIO(opened)})
			return errors.Join(fmt.Errorf("%s: %w", core.ReasonConfiguration, err), cleanupErr)
		}
		opened = append(opened, s)
	}
	if err := m.installHub(u.Name, toWatchIO(opened), cancel, false); err != nil {
		return err
	}
	for _, s := range opened {
		go m.runWatch(ctx, u.Name, "event log subscribe failed", s, m.onEventLogMatch)
	}
	return nil
}

func (m *Manager) onEventLogMatch(name string) {
	m.startHubCompanion(name, func(u *unit.Unit) string {
		if u != nil && u.EventLog != nil {
			return u.EventLog.Unit
		}
		return ""
	})
}
