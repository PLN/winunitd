package manager

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

// TestConcurrentStopThenStartLeavesSecondProcess is issue #24 interleaving A:
// Stop's proc.Stop is slow; Start runs concurrently. Final state must be
// active with the second process, and unitRuntime.proc must be that process.
func TestConcurrentStopThenStartLeavesSecondProcess(t *testing.T) {
	t.Parallel()
	launch := newSlowStopLauncher(80 * time.Millisecond)
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
TimeoutStopSec=30
`,
	})

	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	first := launch.nth(0)
	if first == nil {
		t.Fatal("first process missing")
	}
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.procOfLocked("foo.service") == first
	})

	var stopErr, startErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, stopErr = m.Stop("foo")
	}()
	select {
	case <-launch.stopBegun:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop never entered proc.Stop")
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, startErr = m.Start(context.Background(), "foo")
	}()
	wg.Wait()
	if stopErr != nil {
		t.Fatalf("Stop: %v", stopErr)
	}
	if startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}

	second := launch.nth(1)
	if second == nil {
		t.Fatal("second process missing")
	}
	if first == second {
		t.Fatal("expected a distinct second process")
	}
	m.mu.Lock()
	live := m.procOfLocked("foo.service")
	st := m.stateOfLocked("foo.service")
	m.mu.Unlock()
	if st != core.Active {
		t.Fatalf("state = %s, want active", st)
	}
	if live != second {
		t.Fatalf("unitRuntime.proc is not the second process")
	}
	if !second.Alive() {
		t.Fatal("second process must be alive")
	}
	if first.Alive() {
		t.Fatal("first process must have been stopped")
	}
}

// TestStopMidStartTransactionDoesNotActivateMember is issue #24 interleaving B:
// a slow multi-unit start transaction; stop one member after it has started
// and while another member is still in flight. applyRunLocked must not stamp
// Active onto the stopped member.
func TestStopMidStartTransactionDoesNotActivateMember(t *testing.T) {
	t.Parallel()
	launch := newGatedStartLauncher("slow.service")
	m := managerWith(t, launch, map[string]string{
		"app.target": `
[Unit]
Requires=a.service slow.service
After=a.service slow.service
`,
		"a.service": `
[Service]
Type=simple
ExecStart=C:\Tools\a.exe
WorkingDirectory=C:\Tools
`,
		"slow.service": `
[Unit]
After=a.service
[Service]
Type=simple
ExecStart=C:\Tools\slow.exe
WorkingDirectory=C:\Tools
`,
	})

	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "app.target")
		errc <- err
	}()
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		p := m.procOfLocked("a.service")
		return p != nil && p.Alive()
	})
	select {
	case <-launch.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("slow.service Start was not gated")
	}

	if _, err := m.Stop("a"); err != nil {
		t.Fatalf("Stop a: %v", err)
	}
	assertState(t, m, "a.service", core.Inactive)

	launch.release()
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "superseded by stop") {
			t.Fatalf("Start app.target should report its canceled pending member: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start app.target did not return")
	}

	assertState(t, m, "a.service", core.Inactive)
	m.mu.Lock()
	proc := m.procOfLocked("a.service")
	stopping := m.stoppingOfLocked("a.service")
	m.mu.Unlock()
	if proc != nil && proc.Alive() {
		t.Fatal("stopped member must not keep a live process")
	}
	if !stopping {
		t.Fatal("stopped member must remain stopping so applyRunLocked cannot stamp Active")
	}
	assertState(t, m, "slow.service", core.Active)
	assertState(t, m, "app.target", core.Inactive)
}

type slowStopLauncher struct {
	mu        sync.Mutex
	procs     []*slowStopProc
	delay     time.Duration
	stopBegun chan struct{}
	stopOnce  sync.Once
	nextPID   atomic.Int32
}

func newSlowStopLauncher(delay time.Duration) *slowStopLauncher {
	l := &slowStopLauncher{
		delay:     delay,
		stopBegun: make(chan struct{}),
	}
	l.nextPID.Store(1000)
	return l
}

func (l *slowStopLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	job, err := runtime.OpenUnitJob()
	if err != nil {
		return nil, err
	}
	p := &slowStopProc{
		l:      l,
		pid:    int(l.nextPID.Add(1)),
		job:    job,
		done:   make(chan struct{}),
		stdout: io.NopCloser(strings.NewReader("")),
		stderr: io.NopCloser(strings.NewReader("")),
	}
	l.mu.Lock()
	l.procs = append(l.procs, p)
	l.mu.Unlock()
	return p, nil
}

func (l *slowStopLauncher) nth(i int) *slowStopProc {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i < 0 || i >= len(l.procs) {
		return nil
	}
	return l.procs[i]
}

type slowStopProc struct {
	mu     sync.Mutex
	l      *slowStopLauncher
	pid    int
	job    runtime.Job
	dead   bool
	closed bool
	done   chan struct{}
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func (p *slowStopProc) PID() int              { return p.pid }
func (p *slowStopProc) Job() runtime.Job      { return p.job }
func (p *slowStopProc) Stdout() io.ReadCloser { return p.stdout }
func (p *slowStopProc) Stderr() io.ReadCloser { return p.stderr }
func (p *slowStopProc) ExitCode() (uint32, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dead && !p.closed {
		return 0, false
	}
	return 0, true
}

func (p *slowStopProc) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.dead && !p.closed
}

func (p *slowStopProc) Wait(ctx context.Context) error {
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

func (p *slowStopProc) Stop(timeout time.Duration) error {
	_ = timeout
	p.l.stopOnce.Do(func() { close(p.l.stopBegun) })
	time.Sleep(p.l.delay)
	p.finish()
	if p.job != nil {
		_ = p.job.Kill()
	}
	return p.Close()
}

func (p *slowStopProc) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dead = true
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}

func (p *slowStopProc) Close() error {
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

type gatedStartLauncher struct {
	fakeLauncher
	gate     string
	blocked  chan struct{}
	releaseC chan struct{}
	once     sync.Once
}

func newGatedStartLauncher(gate string) *gatedStartLauncher {
	return &gatedStartLauncher{
		gate:     gate,
		blocked:  make(chan struct{}),
		releaseC: make(chan struct{}),
	}
}

func (g *gatedStartLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if spec.Unit == g.gate {
		g.once.Do(func() { close(g.blocked) })
		select {
		case <-g.releaseC:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.fakeLauncher.Start(ctx, spec)
}

func (g *gatedStartLauncher) release() {
	close(g.releaseC)
}

var _ runtime.Launcher = (*slowStopLauncher)(nil)
var _ runtime.Process = (*slowStopProc)(nil)
var _ runtime.Launcher = (*gatedStartLauncher)(nil)
