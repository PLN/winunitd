package manager

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

func TestRestartAlwaysRelaunchesAfterExit0(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=always
RestartSec=10ms
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	waitStarts(t, launch, 2, 2*time.Second)
}

func TestRestartOnFailureDoesNotRelaunchAfter0(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=on-failure
RestartSec=10ms
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	waitStableStarts(t, launch, 1)
	assertState(t, m, "foo.service", core.Failed)
}

func TestRestartOnFailureRelaunchesAfterNonZero(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(2)}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=on-failure
RestartSec=10ms
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	waitStarts(t, launch, 2, 2*time.Second)
}

func TestRestartNoNeverRelaunches(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(2)}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=no
RestartSec=10ms
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	waitStableStarts(t, launch, 1)
	assertState(t, m, "foo.service", core.Failed)
}

func TestExplicitStopCancelsRestart(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	m := managerWith(t, launch, map[string]string{
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
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.subOfLocked("foo.service") == core.SubAutoRestart
	})
	if _, err := m.Stop("foo"); err != nil {
		t.Fatal(err)
	}
	n := launch.nstarts()
	time.Sleep(200 * time.Millisecond)
	if got := launch.nstarts(); got != n {
		t.Fatalf("relaunched after stop: starts %d -> %d", n, got)
	}
	assertState(t, m, "foo.service", core.Inactive)
}

func TestStopLiveAlwaysStaysDown(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=always
RestartSec=10ms
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
	waitStableStarts(t, launch, 1)
	assertState(t, m, "foo.service", core.Inactive)
}

func TestRestartDoesNotRerunGraph(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitNth: map[int]int{1: 0}}
	m := managerWith(t, launch, map[string]string{
		"web.service": `
[Unit]
Requires=db.service
After=db.service
[Service]
Type=simple
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
Restart=always
RestartSec=10ms
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
	waitUntil(t, 2*time.Second, func() bool {
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
	m := managerWith(t, launch, map[string]string{
		"init.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\init.exe
WorkingDirectory=C:\Tools
Restart=always
RestartSec=10ms
`,
	})
	if _, err := m.Start(context.Background(), "init"); err != nil {
		t.Fatal(err)
	}
	waitStarts(t, launch, 2, 2*time.Second)
}

func TestOneshotRestartOnFailureDoesNotTreatExit0AsCrash(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	m := managerWith(t, launch, map[string]string{
		"init.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\init.exe
WorkingDirectory=C:\Tools
Restart=on-failure
RestartSec=10ms
`,
	})
	if _, err := m.Start(context.Background(), "init"); err != nil {
		t.Fatal(err)
	}
	waitStableStarts(t, launch, 1)
	assertState(t, m, "init.service", core.Active)
}

func TestOneshotRestartOnFailureRelaunchesAfterNonZero(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(3)}
	m := managerWith(t, launch, map[string]string{
		"init.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\init.exe
WorkingDirectory=C:\Tools
Restart=on-failure
RestartSec=10ms
`,
	})
	_, _ = m.Start(context.Background(), "init")
	waitStarts(t, launch, 2, 2*time.Second)
}

func TestDefaultRestartIsNo(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	m := managerWith(t, launch, map[string]string{
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
	waitStableStarts(t, launch, 1)
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
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m
}

func waitStarts(t *testing.T, launch *scriptedLauncher, n int, timeout time.Duration) {
	t.Helper()
	waitUntil(t, timeout, func() bool { return launch.nstarts() >= n })
}

func waitStableStarts(t *testing.T, launch *scriptedLauncher, n int) {
	t.Helper()
	waitUntil(t, time.Second, func() bool { return launch.nstarts() >= n })
	time.Sleep(80 * time.Millisecond)
	if got := launch.nstarts(); got != n {
		t.Fatalf("starts = %d, want %d", got, n)
	}
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
	mu      sync.Mutex
	specs   []runtime.StartSpec
	exitAll *int
	exitNth map[int]int
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
	// Exit after Start returns so graph applyRunLocked does not race watch.
	go func() {
		time.Sleep(15 * time.Millisecond)
		p.die(uint32(code))
	}()
	return p, nil
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
