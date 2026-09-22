package main

import (
	"context"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/winevt"
)

// noteStartupFailure records a failure that happens before the service is
// ready. Cancellation and a running listener are not startup failures.
// When the manager exists, its daemon log is the record and the close path
// drains it. Otherwise this opens that log just long enough to accept one
// event, and reports directly if the log cannot be opened.
func noteStartupFailure(base string, m *manager.Manager, err error) {
	if err == nil || runtime.IsCancellation(err) {
		return
	}
	ev := journal.DaemonEvent{Code: journal.DaemonEventStartupFailed, Reason: err.Error()}
	if m != nil {
		m.RecordDaemonEvent(ev)
		return
	}
	if strings.TrimSpace(base) == "" {
		winevt.EmitStartupFailure(err.Error())
		return
	}
	log, openErr := journal.OpenDaemonLog(base, nil)
	if openErr != nil {
		winevt.EmitStartupFailure(err.Error())
		return
	}
	log.SetEmitter(winevt.Emit)
	log.Record(ev)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = log.CloseContext(ctx)
}
