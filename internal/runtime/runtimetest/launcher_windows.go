//go:build windows

package runtimetest

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

// Launcher succeeds without CreateProcess. Production Windows uses
// runtime.NewLauncher; tests that exercise CLI/protocol without a real
// executable use this so they pass on Windows CI without linking the stub
// into winunitd.exe (issue #33).
func Launcher() runtime.Launcher {
	return LauncherOutput("", "")
}

// LauncherOutput is Launcher with stdout/stderr that the journal can persist.
func LauncherOutput(stdout, stderr string) runtime.Launcher {
	return stubLauncher{stdout: stdout, stderr: stderr}
}

type stubLauncher struct {
	stdout string
	stderr string
}

type stubProc struct {
	mu       sync.Mutex
	pid      int
	job      runtime.Job
	dead     bool
	closed   bool
	exitCode uint32
	exited   bool
	stdout   io.ReadCloser
	stderr   io.ReadCloser
	done     chan struct{}
}

func (l stubLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		return nil, fmt.Errorf("ExecStart is empty")
	}
	if spec.Type == unit.TypeOneshot && spec.TimeoutStart > 0 {
		_ = spec.TimeoutStart
	}
	job, err := runtime.OpenUnitJobWith(spec.Limits)
	if err != nil {
		return nil, err
	}
	return &stubProc{
		pid:    1,
		job:    job,
		stdout: io.NopCloser(strings.NewReader(l.stdout)),
		stderr: io.NopCloser(strings.NewReader(l.stderr)),
		done:   make(chan struct{}),
	}, nil
}

func (p *stubProc) PID() int { return p.pid }

func (p *stubProc) ExitCode() (uint32, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exited && !p.dead && !p.closed {
		return 0, false
	}
	return p.exitCode, true
}

func (p *stubProc) Job() runtime.Job { return p.job }

func (p *stubProc) Stdout() io.ReadCloser { return p.stdout }

func (p *stubProc) Stderr() io.ReadCloser { return p.stderr }

func (p *stubProc) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.dead && !p.closed
}

func (p *stubProc) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		p.mu.Lock()
		code := p.exitCode
		p.mu.Unlock()
		if code == 0 {
			return nil
		}
		return &runtime.ExitStatus{Code: code}
	}
}

func (p *stubProc) Stop(timeout time.Duration) error {
	_ = timeout
	p.finish()
	if p.job != nil {
		_ = p.job.Kill()
	}
	return p.Close()
}

func (p *stubProc) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dead = true
	p.exited = true
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}

func (p *stubProc) Close() error {
	p.finish()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	job := p.job
	stdout := p.stdout
	stderr := p.stderr
	p.stdout = nil
	p.stderr = nil
	p.mu.Unlock()
	if job != nil {
		_ = job.Close()
	}
	if stdout != nil {
		_ = stdout.Close()
	}
	if stderr != nil {
		_ = stderr.Close()
	}
	return nil
}
