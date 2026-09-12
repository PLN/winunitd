package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

// daemonJob is held for the process lifetime so KILL_ON_JOB_CLOSE still fires
// if winunitd is killed. On SCM stop, preshutdown, and console cancel it is
// closed after ordered unit stop so leftover children cannot outlive
// winunitd.exe (DESIGN.md §42, §66).
var daemonJob *runtime.DaemonJob

func serve(ctx context.Context, baseDir string, stderr io.Writer, sessions chan runtime.SessionChange, clock <-chan struct{}) (serveErr error) {
	return serveReady(ctx, baseDir, stderr, sessions, clock, nil)
}

func serveReady(ctx context.Context, baseDir string, stderr io.Writer, sessions chan runtime.SessionChange, clock <-chan struct{}, ready func()) (serveErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	job, err := runtime.OpenBrokerJob()
	if err != nil {
		return err
	}
	daemonJob = job
	if err := job.AssignSelf(); err != nil {
		closeErr := job.Close()
		if closeErr == nil {
			daemonJob = nil
		}
		return errors.Join(fmt.Errorf("assign daemon job: %w", err), closeErr)
	}

	m, err := manager.New(manager.Config{BaseDir: baseDir, Daemon: job})
	if err != nil {
		closeErr := job.Close()
		if closeErr == nil {
			daemonJob = nil
		}
		return errors.Join(err, closeErr)
	}

	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	logf := func(format string, args ...any) {
		fmt.Fprintf(stderr, "winunitd: "+format+"\n", args...)
	}
	runtime.SetLingerLogf(logf)
	admissionPath := filepath.Join(baseDir, "user-admission.json")
	admission, admissionErr := manager.LoadUserAdmission(admissionPath)
	if admissionErr != nil {
		logf("user admission policy: %v", admissionErr)
	}
	host := manager.NewUserHost(manager.UserHostConfig{
		Admission: admission,
		Exe:       exe,
		Daemon:    job,
		LingerDir: filepath.Join(baseDir, "linger"),
		Logf:      logf,
	})
	defer func() { cancel(); serveErr = errors.Join(serveErr, finish(m, job, host, stderr)) }()

	rel, loadErr := m.Reload()
	if loadErr != nil {
		logf("load units: %v; control remains available for repair", loadErr)
	}
	if rel == nil {
		rel = &protocol.DaemonReloadResult{}
	}
	for _, e := range rel.Errors {
		fmt.Fprintf(stderr, "winunitd: %s\n", e)
	}
	if rel.Cycle != "" {
		fmt.Fprintf(stderr, "winunitd: ordering cycle: %s\n", rel.Cycle)
	}

	if sessions == nil {
		sessions = make(chan runtime.SessionChange, 32)
	}
	go runtime.WatchSessions(ctx, sessions)
	go host.Listen(ctx, sessions)
	go host.WatchUserAdmission(ctx, admissionPath)
	go watchClock(ctx, m, clock)

	lis, err := protocol.ListenControl()
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer lis.Close()
	if loadErr == nil {
		logf("loaded %d units, listening on %s", rel.Loaded, protocol.DefaultPipeName)
	} else {
		logf("configuration rejected, listening on %s", protocol.DefaultPipeName)
	}

	ctrl := &manager.Control{Units: m, Users: host}
	serverErr := make(chan error, 1)
	go func() {
		if ready != nil {
			ready()
		}
		err := protocol.Serve(ctx, lis, ctrl, protocol.DefaultAuthorizer())
		cancel() // listener failure also cancels unfinished boot work
		serverErr <- err
	}()
	// Control and SCM readiness precede workload activation. A waiting notify
	// unit must not hide status/stop, and invalid configuration remains repairable.
	if loadErr == nil {
		if _, err := m.Boot(ctx); err != nil {
			logf("start %s: %v", manager.DefaultTarget, err)
		}
	}
	startUserReconciliation(ctx, host)
	return <-serverErr
}

// The main serve path must reach finish even when initial token/account I/O is
// blocked. UserHost tracks accepted native work and rejects late launches after
// shutdown seals admission; this single startup worker needs no replacement.
func startUserReconciliation(ctx context.Context, host *manager.UserHost) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if ctx.Err() == nil {
			host.Reconcile()
		}
		if ctx.Err() == nil {
			host.StartLingering()
		}
	}()
	return done
}

func finish(m *manager.Manager, job *runtime.DaemonJob, host *manager.UserHost, stderr io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), runtime.PreshutdownTimeout)
	defer cancel()
	return finishContext(ctx, m, job, host, stderr)
}

// All shutdown phases share the SCM stop window. Expiration is a failure;
// unfinished native operations retain ownership until completion or process exit.
func finishContext(ctx context.Context, m *manager.Manager, job *runtime.DaemonJob, host *manager.UserHost, stderr io.Writer) error {
	var result error
	record := func(phase string, err error) {
		if err != nil {
			wrapped := fmt.Errorf("%s: %w", phase, err)
			fmt.Fprintf(stderr, "winunitd: %v\n", wrapped)
			result = errors.Join(result, wrapped)
		}
	}
	record("shutdown", manager.ShutdownAll(ctx, m, host))
	if job != nil {
		err := job.CloseContext(ctx)
		record("close daemon job", err)
		if err == nil && daemonJob == job {
			daemonJob = nil
		}
	}
	return errors.Join(result, ctx.Err())
}

func watchClock(ctx context.Context, m *manager.Manager, clock <-chan struct{}) {
	if m == nil || clock == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-clock:
			if !ok {
				return
			}
			m.ClockChanged()
		}
	}
}
