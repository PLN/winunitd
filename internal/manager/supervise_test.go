package manager

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/notify"
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

// TestTCPWatchdogRelaunchClearsTerminated so a delayed Wait on the killed
// proc cannot leave terminated set after Restart= installs the next one
// (issue #62). Type=simple + WatchdogMode=tcp never takes the notify-pipe
// clearer.
func TestTCPWatchdogRelaunchClearsTerminated(t *testing.T) {
	addr := closedLoopbackTCP(t)
	launch := newHoldExitLauncher()
	launch.holdAll = true
	m, fk := managerWithFake(t, launch, map[string]string{
		"tcp.service": fmt.Sprintf(`
[Unit]
StartLimitBurst=0
[Service]
Type=simple
ExecStart=C:\App\tcp.exe
WorkingDirectory=C:\App
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=1s
Restart=always
RestartSec=0
`, addr),
	})
	t.Cleanup(launch.releaseAll)

	if _, err := m.Start(context.Background(), "tcp"); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, time.Second)
	waitCond(t, func() bool { return launch.nstarts() >= 2 })
	assertRelaunchSupervisesExit(t, m, launch, "tcp.service")
}

// TestNotifyWaitReadyRelaunchClearsTerminated is the Type=notify waitReady
// timeout shape of issue #62: terminated is set on READY=1 failure, then
// Restart= relaunches while the old Wait is still held.
func TestNotifyWaitReadyRelaunchClearsTerminated(t *testing.T) {
	launch := newHoldExitLauncher()
	launch.holdAll = true
	launch.pid = os.Getpid()
	m, fk := managerWithFake(t, launch, map[string]string{
		"late.service": `
[Unit]
StartLimitBurst=0
[Service]
Type=notify
ExecStart=C:\App\late.exe
WorkingDirectory=C:\App
TimeoutStartSec=7s
Restart=always
RestartSec=1s
`,
	})
	t.Cleanup(launch.releaseAll)

	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "late")
		errc <- err
	}()
	waitCond(t, func() bool { return launch.nstarts() >= 1 })
	// Keep readiness distinct from the default 5s journal/stop deadline.
	waitCond(t, func() bool { return fk.WaitingAt(7 * time.Second) })
	fk.Advance(7 * time.Second)
	if err := waitErr(t, errc); err == nil {
		t.Fatal("expected TimeoutStartSec failure")
	}
	// RestartSec>0 so beginRestart sits in SubAutoRestart until Start's
	// applyRunLocked returns. RestartSec=0 can install the new proc while
	// applyRunLocked still stamps the failed Start (Failed over
	// Activating/start; reapFailed then kills it).
	waitSub(t, m, "late.service", core.SubAutoRestart)
	advanceArmed(t, fk, time.Second)
	waitCond(t, func() bool { return launch.nstarts() >= 2 })
	pipe := notifyPipeOf(t, launch, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notify.SendRetry(ctx, pipe, notify.Message{Ready: true}); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "late.service", core.Active)
	assertRelaunchSupervisesExit(t, m, launch, "late.service")
}

func assertRelaunchSupervisesExit(t *testing.T, m *Manager, launch *holdExitLauncher, name string) {
	t.Helper()
	first := launch.first()
	second := launch.nth(1)
	if first == nil || second == nil {
		t.Fatal("need two procs (timeout then relaunch)")
	}
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		rt := m.units[name]
		return rt != nil && rt.proc == second
	})
	m.mu.Lock()
	stuck := m.units[name] != nil && m.units[name].terminated
	m.mu.Unlock()
	if stuck {
		t.Fatal("terminated must be false after relaunch installs the new proc (issue #62)")
	}
	// Old Wait returns after the new proc is installed, the same window as
	// the 150 ms Job Object limit-check on Windows.
	first.releaseWait()
	second.die()
	second.releaseWait()
	waitCond(t, func() bool {
		if launch.nstarts() >= 3 {
			return true
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		rt := m.units[name]
		if rt == nil {
			return false
		}
		return rt.state == core.Failed || rt.sub == core.SubAutoRestart
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[name]
	if rt == nil {
		t.Fatal("runtime missing")
	}
	if rt.state == core.Active && rt.proc == nil {
		t.Fatal("issue #62: Active with nil proc; terminated stuck across relaunch")
	}
}

func notifyPipeOf(t *testing.T, launch *holdExitLauncher, idx int) string {
	t.Helper()
	var pipe string
	waitCond(t, func() bool {
		specs := launch.specs()
		if idx < 0 || idx >= len(specs) {
			return false
		}
		v, ok := notify.LookupEnv(specs[idx].Env, notify.EnvNotifyPipe)
		if !ok || v == "" {
			return false
		}
		pipe = v
		return true
	})
	return pipe
}

type holdExitLauncher struct {
	mu      sync.Mutex
	n       int
	held    []*holdProc
	firstP  *holdProc
	holdAll bool
	pid     int
	starts  []runtime.StartSpec
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
	if l.pid != 0 {
		p.pid = l.pid
	}
	l.mu.Lock()
	l.n++
	l.starts = append(l.starts, spec)
	if l.n == 1 || l.holdAll {
		p.holdAfterExit = true
	}
	if l.n == 1 {
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

func (l *holdExitLauncher) nth(i int) *holdProc {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i < 0 || i >= len(l.held) {
		return nil
	}
	return l.held[i]
}

func (l *holdExitLauncher) specs() []runtime.StartSpec {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]runtime.StartSpec, len(l.starts))
	copy(out, l.starts)
	return out
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
	if !p.holdAfterExit {
		p.releaseWait()
	}
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
