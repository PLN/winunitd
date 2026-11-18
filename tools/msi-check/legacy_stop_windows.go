//go:build windows

package main

import (
	"context"
	"path/filepath"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

// legacySnapshotBudget bounds the whole read-only snapshot call: dial,
// request and reply.
const legacySnapshotBudget = 15 * time.Second

// legacyStopPreflight refuses a major upgrade over a running legacy manager
// while a system unit could need a fresh ExecStop in its shutdown. It reads
// one snapshot over the system control pipe and system unit files. It sends
// no lifecycle request, so a refusal leaves the manager and its units as
// they were.
func legacyStopPreflight(ctx context.Context, release, dataDir string) error {
	snap, err := readManagerSnapshot(ctx)
	return legacyStopConflict(release, snap, err, filepath.Join(dataDir, "units"))
}

func readManagerSnapshot(ctx context.Context) (*protocol.SnapshotResult, error) {
	ctx, cancel := context.WithTimeout(ctx, legacySnapshotBudget)
	defer cancel()
	conn, err := protocol.DialDefault(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// The client does not apply ctx to pipe I/O; give the pipe the same end.
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	return protocol.NewClient(conn).Snapshot(ctx)
}
