package manager

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
	"github.com/PLN/winunitd/internal/unit"
)

func TestRestartAlwaysRelaunchesAfterExit0(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0), holdAutoExit: true}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=always
RestartSec=5s
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	launch.releaseExits()
	waitSub(t, m, "foo.service", core.SubAutoRestart)
	advanceWait(t, fk, 5*time.Second)
	waitCond(t, func() bool { return launch.nstarts() >= 2 })
}

func TestRestartOnFailureDoesNotRelaunchAfter0(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0), holdAutoExit: true}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=on-failure
RestartSec=5s
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	launch.releaseExits()
	waitState(t, m, "foo.service", core.Failed)
	fk.Advance(5 * time.Second)
	if got := launch.nstarts(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
	assertState(t, m, "foo.service", core.Failed)
}

func TestRestartOnFailureRelaunchesAfterNonZero(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(2), holdAutoExit: true}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=on-failure
RestartSec=5s
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	launch.releaseExits()
	waitSub(t, m, "foo.service", core.SubAutoRestart)
	advanceWait(t, fk, 5*time.Second)
	waitCond(t, func() bool { return launch.nstarts() >= 2 })
}

func TestRestartNoNeverRelaunches(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(2), holdAutoExit: true}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=no
RestartSec=5s
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	launch.releaseExits()
	waitState(t, m, "foo.service", core.Failed)
	fk.Advance(5 * time.Second)
	if got := launch.nstarts(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
	assertState(t, m, "foo.service", core.Failed)
}

func TestExplicitStopCancelsRestart(t *testing.T) {
	t.Parallel()
	testRestartDelayCancelledAtEpsilon(t, false)
}

func TestRestartDelayCancelledAtEpsilon(t *testing.T) {
	t.Parallel()
	testRestartDelayCancelledAtEpsilon(t, true)
}

func testRestartDelayCancelledAtEpsilon(t *testing.T, atEpsilon bool) {
	t.Helper()
	launch := &scriptedLauncher{exitAll: intPtr(0), holdAutoExit: true}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=always
RestartSec=1s
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	launch.releaseExits()
	waitSub(t, m, "foo.service", core.SubAutoRestart)
	if atEpsilon {
		advanceWait(t, fk, time.Millisecond)
	}
	if _, err := m.Stop("foo"); err != nil {
		t.Fatal(err)
	}
	n := launch.nstarts()
	if fk.Waiting() {
		advanceWait(t, fk, time.Second)
	}
	if got := launch.nstarts(); got != n {
		t.Fatalf("relaunched after stop: starts %d -> %d", n, got)
	}
	assertState(t, m, "foo.service", core.Inactive)
}

func TestStopLiveAlwaysStaysDown(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=always
RestartSec=5s
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	if launch.nstarts() != 1 {
		t.Fatalf("starts = %d", launch.nstarts())
	}
	if _, err := m.Stop("foo"); err != nil {
		t.Fatal(err)
	}
	fk.Advance(5 * time.Second)
	if got := launch.nstarts(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
	assertState(t, m, "foo.service", core.Inactive)
}

func TestRestartDoesNotRerunGraph(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitNth: map[int]int{1: 0}, holdAutoExit: true}
	m, fk := managerWithFake(t, launch, map[string]string{
		"web.service": `
[Unit]
Requires=db.service
After=db.service
[Service]
Type=simple
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
Restart=always
RestartSec=5s
`,
		"db.service": `
[Service]
Type=simple
ExecStart=C:\Tools\db.exe
WorkingDirectory=C:\Tools
Restart=no
`,
	})
	if _, err := m.Start(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	launch.releaseExits()
	waitSub(t, m, "web.service", core.SubAutoRestart)
	advanceWait(t, fk, 5*time.Second)
	waitCond(t, func() bool {
		nweb := 0
		for _, name := range launch.units() {
			if name == "web.service" {
				nweb++
			}
		}
		return nweb >= 2
	})
	ndb := 0
	for _, name := range launch.units() {
		if name == "db.service" {
			ndb++
		}
	}
	if ndb != 1 {
		t.Fatalf("db starts = %d (restart must not re-run Requires=)", ndb)
	}
	got := launch.units()
	if len(got) < 2 || got[0] != "db.service" || got[1] != "web.service" {
		t.Fatalf("first start order = %v", got)
	}
}

func TestOneshotRestartAlwaysRelaunchesAfter0(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	m, fk := managerWithFake(t, launch, map[string]string{
		"init.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\init.exe
WorkingDirectory=C:\Tools
Restart=always
RestartSec=5s
`,
	})
	if _, err := m.Start(context.Background(), "init"); err != nil {
		t.Fatal(err)
	}
	waitSub(t, m, "init.service", core.SubAutoRestart)
	advanceWait(t, fk, 5*time.Second)
	waitCond(t, func() bool { return launch.nstarts() >= 2 })
}

func TestOneshotRestartOnFailureDoesNotTreatExit0AsCrash(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	m, fk := managerWithFake(t, launch, map[string]string{
		"init.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\init.exe
WorkingDirectory=C:\Tools
Restart=on-failure
RestartSec=5s
`,
	})
	if _, err := m.Start(context.Background(), "init"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "init.service", core.Active)
	fk.Advance(5 * time.Second)
	if got := launch.nstarts(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
	assertState(t, m, "init.service", core.Active)
}

func TestOneshotRestartOnFailureRelaunchesAfterNonZero(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(3)}
	m, fk := managerWithFake(t, launch, map[string]string{
		"init.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\init.exe
WorkingDirectory=C:\Tools
Restart=on-failure
RestartSec=5s
`,
	})
	_, _ = m.Start(context.Background(), "init")
	waitSub(t, m, "init.service", core.SubAutoRestart)
	advanceWait(t, fk, 5*time.Second)
	waitCond(t, func() bool { return launch.nstarts() >= 2 })
}

func TestDefaultRestartIsNo(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0), holdAutoExit: true}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	launch.releaseExits()
	waitState(t, m, "foo.service", core.Failed)
	fk.Advance(5 * time.Second)
	if got := launch.nstarts(); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
}

func TestClassifyWait(t *testing.T) {
	t.Parallel()
	if classifyWait(nil) != core.ExitSuccess {
		t.Fatal("nil")
	}
	if classifyWait(&runtime.ExitStatus{Code: 0}) != core.ExitSuccess {
		t.Fatal("zero")
	}
	if classifyWait(&runtime.ExitStatus{Code: 2}) != core.ExitFailure {
		t.Fatal("nonzero")
	}
	if classifyWait(fmt.Errorf("wait failed")) != core.ExitAbnormal {
		t.Fatal("abnormal")
	}
}

func managerWith(t *testing.T, launch runtime.Launcher, files map[string]string) *Manager {
	t.Helper()
	return managerWithClock(t, launch, timers.Clock{}, files)
}

func waitUntil(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout")
}

func assertState(t *testing.T, m *Manager, name string, want core.State) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if got := m.stateOfLocked(name); got != want {
		t.Fatalf("state = %s, want %s", got, want)
	}
}

func intPtr(n int) *int { return &n }

type scriptedLauncher struct {
	mu           sync.Mutex
	specs        []runtime.StartSpec
	at           []time.Time
	exitAll      *int
	exitNth      map[int]int
	holdAutoExit bool
	pending      []*pendingExit
}

type pendingExit struct {
	p    *fakeProc
	code uint32
}

func (s *scriptedLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	idx := len(s.specs)
	s.specs = append(s.specs, spec)
	s.at = append(s.at, time.Now())
	code, auto := s.codeForLocked(idx)
	s.mu.Unlock()

	job, err := runtime.OpenUnitJob()
	if err != nil {
		return nil, err
	}
	p := &fakeProc{
		job:    job,
		done:   make(chan struct{}),
		stdout: io.NopCloser(strings.NewReader("")),
		stderr: io.NopCloser(strings.NewReader("")),
	}
	if !auto {
		return p, nil
	}
	if spec.Type == unit.TypeOneshot {
		p.die(uint32(code))
		if code != 0 {
			_ = p.Close()
			return nil, &runtime.ExitStatus{Code: uint32(code)}
		}
		return p, nil
	}
	if s.holdAutoExit {
		s.mu.Lock()
		s.pending = append(s.pending, &pendingExit{p: p, code: uint32(code)})
		s.mu.Unlock()
		return p, nil
	}
	// Exit after Start returns so graph applyRunLocked does not race watch.
	// Unconverted tests still use a short real delay (see ## Unverified).
	go func() {
		time.Sleep(15 * time.Millisecond)
		p.die(uint32(code))
	}()
	return p, nil
}

func (s *scriptedLauncher) releaseExits() {
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	for _, pe := range pending {
		pe.p.die(pe.code)
	}
}

func (s *scriptedLauncher) codeForLocked(idx int) (int, bool) {
	if s.exitAll != nil {
		return *s.exitAll, true
	}
	if s.exitNth != nil {
		if code, ok := s.exitNth[idx]; ok {
			return code, true
		}
	}
	return 0, false
}

func (s *scriptedLauncher) nstarts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.specs)
}

func (s *scriptedLauncher) startTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Time, len(s.at))
	copy(out, s.at)
	return out
}

func (s *scriptedLauncher) units() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.specs))
	for i, spec := range s.specs {
		out[i] = spec.Unit
	}
	return out
}

func (p *fakeProc) die(code uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exitCode = code
	p.dead = true
	p.exited = true
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}
