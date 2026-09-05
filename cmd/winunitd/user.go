package main

import (
	"context"
	"fmt"
	"io"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

// Tests replace this factory only inside their test-binary subprocesses.
// Production has no environment or command-line endpoint override.
var listenUserControl = protocol.ListenUserControl

// serveUser runs this process as a per-user manager for sid (same binary,
// not an SCM service). Units load from %LOCALAPPDATA%\winunitd\units\.
func serveUser(ctx context.Context, sid, baseDir string, stderr io.Writer) error {
	if !protocol.ValidSID(sid) {
		return fmt.Errorf("invalid SID %q", sid)
	}

	info, err := runtime.CurrentUserInfo()
	if err != nil {
		return fmt.Errorf("user manager identity: %w", err)
	}
	if info.SID != sid {
		return fmt.Errorf("user manager SID mismatch: running as %s, want %s", info.SID, sid)
	}
	if err := runtime.ApplyUserEnv(info); err != nil {
		fmt.Fprintf(stderr, "winunitd: apply user env: %v\n", err)
	}
	if baseDir == "" {
		baseDir = manager.DefaultUserBaseDir()
	}
	if err := runtime.EnsureDataDirs(baseDir); err != nil {
		return err
	}

	job, err := runtime.OpenDaemonJob()
	if err != nil {
		return err
	}
	daemonJob = job
	if err := job.AssignSelf(); err != nil {
		fmt.Fprintf(stderr, "winunitd: assign user manager job: %v\n", err)
	}

	m, err := manager.New(manager.Config{
		BaseDir:   baseDir,
		Daemon:    job,
		UserScope: true,
		NotifySID: sid,
		HasInteractiveSession: func() bool {
			return runtime.SIDHasInteractiveSession(sid)
		},
	})
	if err != nil {
		_ = job.Close()
		daemonJob = nil
		return err
	}
	defer finish(m, job, nil, stderr)

	rel, err := m.Reload()
	if err != nil {
		return fmt.Errorf("load units: %w", err)
	}
	for _, e := range rel.Errors {
		fmt.Fprintf(stderr, "winunitd: %s\n", e)
	}
	if rel.Cycle != "" {
		fmt.Fprintf(stderr, "winunitd: ordering cycle: %s\n", rel.Cycle)
	}

	if _, err := m.Boot(ctx); err != nil {
		fmt.Fprintf(stderr, "winunitd: start %s: %v\n", manager.DefaultTarget, err)
	}
	if err := m.SyncGraphicalSession(ctx); err != nil {
		fmt.Fprintf(stderr, "winunitd: %s: %v\n", manager.GraphicalSessionTarget, err)
	}

	lis, err := listenUserControl(sid)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer lis.Close()
	fmt.Fprintf(stderr, "winunitd: user manager %s loaded %d units, listening on %s\n", sid, rel.Loaded, lis.Addr())

	ch := make(chan runtime.SessionChange, 32)
	go runtime.WatchSessions(ctx, ch)
	go m.WatchGraphicalSession(ctx, ch)

	return protocol.Serve(ctx, lis, m, protocol.UserAuthorizer(sid))
}
