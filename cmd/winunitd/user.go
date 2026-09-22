//lint:file-ignore SA4023 Non-Windows native API stubs always return errors; shared callers must check Windows results.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/winevt"
)

// Tests replace this factory only inside their test-binary subprocesses.
// Production has no environment or command-line endpoint override.
var listenUserControl = protocol.ListenUserControl

// serveUser runs this process as a per-user manager for sid (same binary,
// not an SCM service). Units load from %LOCALAPPDATA%\winunitd\units\.
func serveUser(ctx context.Context, sid, baseDir string, stderr io.Writer) (serveErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if !protocol.ValidSID(sid) {
		err := fmt.Errorf("invalid SID %q", sid)
		noteStartupFailure(baseDir, nil, err)
		return err
	}

	info, err := runtime.CurrentUserInfo()
	if err != nil {
		err = fmt.Errorf("user manager identity: %w", err)
		noteStartupFailure(baseDir, nil, err)
		return err
	}
	if info.SID != sid {
		err = fmt.Errorf("user manager SID mismatch: running as %s, want %s", info.SID, sid)
		noteStartupFailure(baseDir, nil, err)
		return err
	}
	if err := runtime.ApplyUserEnv(info); err != nil {
		fmt.Fprintf(stderr, "winunitd: apply user env: %v\n", err)
	}
	if err := runtime.SetHeadlessUserWorkingDirectory(info.Profile); err != nil {
		err = fmt.Errorf("headless user working directory: %w", err)
		noteStartupFailure(baseDir, nil, err)
		return err
	}
	if baseDir == "" {
		baseDir = manager.DefaultUserBaseDir()
	}
	if err := runtime.EnsureDataDirs(baseDir); err != nil {
		noteStartupFailure(baseDir, nil, err)
		return err
	}

	job, err := runtime.OpenDaemonJob()
	if err != nil {
		noteStartupFailure(baseDir, nil, err)
		return err
	}
	daemonJob = job
	if err := job.AssignSelf(); err != nil {
		closeErr := job.Close()
		if closeErr == nil {
			daemonJob = nil
		}
		err = errors.Join(fmt.Errorf("assign user manager job: %w", err), closeErr)
		noteStartupFailure(baseDir, nil, err)
		return err
	}

	m, err := manager.New(manager.Config{
		BaseDir:         baseDir,
		Daemon:          job,
		UserScope:       true,
		NotifySID:       sid,
		DaemonEventEmit: winevt.Emit,
		HasInteractiveSession: func() bool {
			return runtime.SIDHasInteractiveSession(sid)
		},
	})
	if err != nil {
		closeErr := job.Close()
		if closeErr == nil {
			daemonJob = nil
		}
		err = errors.Join(err, closeErr)
		noteStartupFailure(baseDir, nil, err)
		return err
	}
	defer func() { cancel(); serveErr = errors.Join(serveErr, finish(m, job, nil, stderr)) }()

	rel, err := m.Reload()
	if err != nil {
		err = fmt.Errorf("load units: %w", err)
		noteStartupFailure(baseDir, m, err)
		return err
	}
	for _, e := range rel.Errors {
		fmt.Fprintf(stderr, "winunitd: %s\n", e)
	}
	if rel.Cycle != "" {
		fmt.Fprintf(stderr, "winunitd: ordering cycle: %s\n", rel.Cycle)
	}

	lis, err := listenUserControl(sid)
	if err != nil {
		err = fmt.Errorf("listen: %w", err)
		noteStartupFailure(baseDir, m, err)
		return err
	}
	defer lis.Close()
	fmt.Fprintf(stderr, "winunitd: user manager %s loaded %d units, listening on %s\n", sid, rel.Loaded, lis.Addr())

	serverErr := make(chan error, 1)
	go func() {
		err := protocol.Serve(ctx, lis, m, protocol.UserAuthorizer(sid))
		cancel() // listener failure also cancels unfinished boot work
		serverErr <- err
	}()
	// As in the SYSTEM manager, pending readiness must not hide status/stop
	// or prevent a boot workload from consulting its own control endpoint.
	if _, err := m.Boot(ctx); err != nil {
		fmt.Fprintf(stderr, "winunitd: start %s: %v\n", manager.DefaultTarget, err)
	}
	if err := m.SyncGraphicalSession(ctx); err != nil {
		fmt.Fprintf(stderr, "winunitd: %s: %v\n", manager.GraphicalSessionTarget, err)
	}

	ch := make(chan runtime.SessionChange, 32)
	go runtime.WatchSessions(ctx, ch)
	go m.WatchGraphicalSession(ctx, ch)

	return <-serverErr
}
