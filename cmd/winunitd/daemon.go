package main

import (
	"context"
	"fmt"
	"io"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

// daemonJob is held for the process lifetime so KILL_ON_JOB_CLOSE still fires
// if winunitd is killed. The handle is not closed on sc stop (M10).
var daemonJob *runtime.DaemonJob

func serve(ctx context.Context, baseDir string, stderr io.Writer) error {
	job, err := runtime.OpenDaemonJob()
	if err != nil {
		return err
	}
	daemonJob = job
	if err := job.AssignSelf(); err != nil {
		fmt.Fprintf(stderr, "winunitd: assign daemon job: %v\n", err)
	}

	m, err := manager.New(manager.Config{BaseDir: baseDir, Daemon: job})
	if err != nil {
		return err
	}
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

	lis, err := protocol.ListenControl()
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer lis.Close()
	fmt.Fprintf(stderr, "winunitd: loaded %d units, listening on %s\n", rel.Loaded, protocol.DefaultPipeName)

	return protocol.Serve(ctx, lis, m, protocol.DefaultAuthorizer())
}
