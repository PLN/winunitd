package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

// daemonJob is held for the process lifetime so KILL_ON_JOB_CLOSE still fires
// if winunitd is killed. On SCM stop, preshutdown, and console cancel it is
// closed after ordered unit stop so leftover children cannot outlive
// winunitd.exe (DESIGN.md §42, §66).
var daemonJob *runtime.DaemonJob

func serve(ctx context.Context, baseDir string, stderr io.Writer, sessions chan runtime.SessionChange) error {
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
		_ = job.Close()
		daemonJob = nil
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	host := manager.NewUserHost(manager.UserHostConfig{
		Exe:    exe,
		Daemon: job,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(stderr, "winunitd: "+format+"\n", args...)
		},
	})
	defer finish(m, job, host, stderr)

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

	if sessions == nil {
		sessions = make(chan runtime.SessionChange, 32)
	}
	go runtime.WatchSessions(ctx, sessions)
	go host.Listen(ctx, sessions)

	lis, err := protocol.ListenControl()
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer lis.Close()
	fmt.Fprintf(stderr, "winunitd: loaded %d units, listening on %s\n", rel.Loaded, protocol.DefaultPipeName)

	return protocol.Serve(ctx, lis, m, protocol.DefaultAuthorizer())
}

func finish(m *manager.Manager, job *runtime.DaemonJob, host *manager.UserHost, stderr io.Writer) {
	if host != nil {
		host.Close()
	}
	if err := m.Shutdown(context.Background()); err != nil {
		fmt.Fprintf(stderr, "winunitd: shutdown: %v\n", err)
	}
	m.Close()
	if job != nil {
		if err := job.Close(); err != nil {
			fmt.Fprintf(stderr, "winunitd: close daemon job: %v\n", err)
		}
	}
	daemonJob = nil
}
