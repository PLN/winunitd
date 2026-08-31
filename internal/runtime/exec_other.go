//go:build !windows

package runtime

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/PLN/winunitd/internal/unit"
)

type stubLauncher struct{}

func newLauncher(daemon *DaemonJob) Launcher {
	return stubLauncher{}
}

type stubProc struct {
	mu     sync.Mutex
	pid    int
	job    *UnitJob
	dead   bool
	closed bool
	stdout io.ReadCloser
	stderr io.ReadCloser
	done   chan struct{}
}

func (stubLauncher) Start(ctx context.Context, spec StartSpec) (Process, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		return nil, fmt.Errorf("ExecStart is empty")
	}
	if spec.Type == unit.TypeOneshot && spec.TimeoutStart > 0 {
		// Linux stub does not run the process. A oneshot "start" succeeds
		// immediately so protocol tests can still mark the unit active.
		_ = spec.TimeoutStart
	}
	job, err := OpenUnitJob()
	if err != nil {
		return nil, err
	}
	return &stubProc{
		pid:    0,
		job:    job,
		stdout: io.NopCloser(strings.NewReader("")),
		stderr: io.NopCloser(strings.NewReader("")),
		done:   make(chan struct{}),
	}, nil
}

func (p *stubProc) PID() int { return p.pid }

func (p *stubProc) Job() Job { return p.job }

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
		return nil
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
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}

func (p *stubProc) Close() error {
	p.finish()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.job != nil {
		_ = p.job.Close()
	}
	if p.stdout != nil {
		_ = p.stdout.Close()
	}
	if p.stderr != nil {
		_ = p.stderr.Close()
	}
	return nil
}
