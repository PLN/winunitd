package manager

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

// TestLaunchUnitEvictsDeadProcOwnsJobTeardown forces the issue #25 window:
// Alive() is false, watch is still blocked in Wait, and an immediate Start
// evicts the unreaped proc. The evictor must job.Kill() and Close; watch's
// later early return must still Close (idempotent).
func TestLaunchUnitEvictsDeadProcOwnsJobTeardown(t *testing.T) {
	t.Parallel()
	launch := newHoldExitLauncher()
	m := managerWith(t, launch, map[string]string{
		"tree.service": `
[Service]
Type=simple
ExecStart=C:\Tools\tree.exe
WorkingDirectory=C:\Tools
Restart=no
`,
	})
	t.Cleanup(launch.releaseAll)

	if _, err := m.Start(context.Background(), "tree"); err != nil {
		t.Fatal(err)
	}
	first := launch.first()
	if first == nil {
		t.Fatal("first proc missing")
	}
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.procOfLocked("tree.service") == first
	})

	first.die()
	if first.Alive() {
		t.Fatal("parent must be dead before the second Start")
	}
	if first.closedCount() != 0 {
		t.Fatal("watch must not have closed yet (Wait is held)")
	}

	if _, err := m.Start(context.Background(), "tree"); err != nil {
		t.Fatal(err)
	}
	if launch.nstarts() != 2 {
		t.Fatalf("starts = %d, want 2", launch.nstarts())
	}
	if first.job.kills() < 1 {
		t.Fatal("evicting a dead proc must job.Kill() (issue #25)")
	}
	if first.closedCount() < 1 {
		t.Fatal("evicting a dead proc must Close handles (issue #25)")
	}

	m.mu.Lock()
	live := m.procOfLocked("tree.service")
	m.mu.Unlock()
	if live == nil || live == first || !live.Alive() {
		t.Fatal("second Start must install a new live proc")
	}

	first.releaseWait()
	waitCond(t, func() bool { return first.closedCount() >= 2 })
	assertState(t, m, "tree.service", core.Active)
}

type holdExitLauncher struct {
	mu     sync.Mutex
	n      int
	held   []*holdProc
	firstP *holdProc
}

func newHoldExitLauncher() *holdExitLauncher {
	return &holdExitLauncher{}
}

func (l *holdExitLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	inner, err := runtime.OpenUnitJob()
	if err != nil {
		return nil, err
	}
	job := newRecJob(inner)
	p := newHoldProc(job)
	l.mu.Lock()
	l.n++
	if l.n == 1 {
		p.holdAfterExit = true
		l.firstP = p
	}
	l.held = append(l.held, p)
	l.mu.Unlock()
	return p, nil
}

func (l *holdExitLauncher) nstarts() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}

func (l *holdExitLauncher) first() *holdProc {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.firstP
}

func (l *holdExitLauncher) releaseAll() {
	l.mu.Lock()
	held := append([]*holdProc(nil), l.held...)
	l.mu.Unlock()
	for _, p := range held {
		p.releaseWait()
	}
}

type recJob struct {
	runtime.Job
	mu     sync.Mutex
	nkills int
}

func newRecJob(inner runtime.Job) *recJob {
	return &recJob{Job: inner}
}

func (j *recJob) Kill() error {
	j.mu.Lock()
	j.nkills++
	j.mu.Unlock()
	if j.Job != nil {
		return j.Job.Kill()
	}
	return nil
}

func (j *recJob) kills() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.nkills
}

type holdProc struct {
	mu            sync.Mutex
	pid           int
	job           *recJob
	dead          bool
	closed        bool
	nclose        int
	exitCode      uint32
	exited        bool
	done          chan struct{}
	hold          chan struct{}
	holdAfterExit bool
	stdout        io.ReadCloser
	stderr        io.ReadCloser
	relOnce       sync.Once
}

func newHoldProc(job *recJob) *holdProc {
	return &holdProc{
		pid:    1,
		job:    job,
		done:   make(chan struct{}),
		hold:   make(chan struct{}),
		stdout: io.NopCloser(strings.NewReader("")),
		stderr: io.NopCloser(strings.NewReader("")),
	}
}

func (p *holdProc) PID() int { return p.pid }

func (p *holdProc) ExitCode() (uint32, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exited && !p.dead && !p.closed {
		return 0, false
	}
	return p.exitCode, true
}

func (p *holdProc) Job() runtime.Job { return p.job }

func (p *holdProc) Stdout() io.ReadCloser { return p.stdout }

func (p *holdProc) Stderr() io.ReadCloser { return p.stderr }

func (p *holdProc) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.dead && !p.closed
}

func (p *holdProc) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
	}
	if p.holdAfterExit {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.hold:
		}
	}
	p.mu.Lock()
	code := p.exitCode
	p.mu.Unlock()
	if code == 0 {
		return nil
	}
	return &runtime.ExitStatus{Code: code}
}

func (p *holdProc) Stop(timeout time.Duration) error {
	_ = timeout
	p.die()
	p.releaseWait()
	if p.job != nil {
		_ = p.job.Kill()
	}
	return p.Close()
}

func (p *holdProc) die() {
	p.mu.Lock()
	p.dead = true
	p.exited = true
	p.mu.Unlock()
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}

func (p *holdProc) releaseWait() {
	p.relOnce.Do(func() { close(p.hold) })
}

func (p *holdProc) closedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.nclose
}

func (p *holdProc) Close() error {
	p.mu.Lock()
	p.nclose++
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.dead = true
	job := p.job
	stdout := p.stdout
	stderr := p.stderr
	p.stdout = nil
	p.stderr = nil
	p.mu.Unlock()
	select {
	case <-p.done:
	default:
		close(p.done)
	}
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

var _ runtime.Process = (*holdProc)(nil)
var _ runtime.Launcher = (*holdExitLauncher)(nil)
var _ runtime.Job = (*recJob)(nil)
