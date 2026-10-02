//go:build windows

package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/runtime/runtimetest/nestedjob"
	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows"
)

// Workload-created nested jobs (#265), owner lane with a real Manager and the
// Windows launcher. Rows refer to the nestedjob case matrix.

const nestedUnitName = "nested.service"

// nestedLauncher wraps the real launcher. Each workload launch records which
// previous-generation processes were still running at that moment: the
// replacement-acceptance boundary in this lane. The optional seam wraps the
// workload in failedStopProcess so a test can fail its first cleanup.
type nestedLauncher struct {
	inner runtime.Launcher
	seam  bool
	lie   bool

	mu         sync.Mutex
	mains      int
	previous   []*nestedjob.Held
	violations []string
	failing    *failedStopProcess
}

func (l *nestedLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	helper := len(spec.Argv) > 2 && spec.Argv[2] == nestedjob.RoleStop
	if !helper {
		l.mu.Lock()
		l.mains++
		for _, h := range l.previous {
			if done, err := h.Signaled(); err != nil || !done {
				l.violations = append(l.violations, fmt.Sprintf("%s pid %d running at replacement launch (%v)", h.ID.Role, h.ID.PID, err))
			}
		}
		l.mu.Unlock()
	}
	p, err := l.inner.Start(ctx, spec)
	if helper || !l.seam || p == nil {
		return p, err
	}
	f := &failedStopProcess{Process: p, lie: l.lie}
	f.fail.Store(true)
	l.mu.Lock()
	l.failing = f
	l.mu.Unlock()
	return f, err
}

func (l *nestedLauncher) expectDrained(held []*nestedjob.Held) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.previous = append([]*nestedjob.Held(nil), held...)
}

func (l *nestedLauncher) state() (int, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mains, append([]string(nil), l.violations...)
}

type nestedManagerCase struct {
	m      *Manager
	obs    *nestedjob.Observer
	launch *nestedLauncher
	cfg    nestedjob.MainConfig
}

func nestedArgvJSON(t *testing.T, args []string) string {
	t.Helper()
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(append([]string{exe, nestedjob.HelperSelector}, args...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeNestedUnit renders nested.service for cfg under base\units.
func writeNestedUnit(t *testing.T, base string, cfg nestedjob.MainConfig, unitLines, serviceLines string, stop *nestedjob.StopConfig) {
	t.Helper()
	units := filepath.Join(base, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[Unit]\nStartLimitBurst=0\n" + unitLines +
		"[Service]\nType=simple\nExecStart=" + nestedArgvJSON(t, cfg.Args()) + "\n" +
		"WorkingDirectory=" + cfg.CaseDir + "\n" + serviceLines
	if stop != nil {
		body += "ExecStop=" + nestedArgvJSON(t, stop.Args()) + "\n"
	}
	if err := os.WriteFile(filepath.Join(units, nestedUnitName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func nestedCaseConfig(t *testing.T, base, mode string) nestedjob.MainConfig {
	t.Helper()
	dir := filepath.Join(base, "case")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return nestedjob.MainConfig{LaunchMode: mode, CaseDir: dir, Generation: 1, Work: nestedjob.WorkIdle, OnStop: nestedjob.OnStopCooperative}
}

// startNestedManager starts nested.service in a fresh manager. Cleanup stops
// the unit, terminates anything that escaped and releases held handles.
func startNestedManager(t *testing.T, launch *nestedLauncher, cfg nestedjob.MainConfig, base, unitLines, serviceLines string, stop *nestedjob.StopConfig) *nestedManagerCase {
	t.Helper()
	writeNestedUnit(t, base, cfg, unitLines, serviceLines, stop)
	launch.inner = runtime.DefaultLauncher()
	m, err := New(Config{BaseDir: base, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	c := &nestedManagerCase{m: m, obs: nestedjob.NewObserver(cfg.CaseDir), launch: launch, cfg: cfg}
	t.Cleanup(func() {
		launch.mu.Lock()
		if launch.failing != nil {
			launch.failing.fail.Store(false)
		}
		launch.mu.Unlock()
		_, _ = m.Stop(nestedUnitName)
		m.Close()
		if err := c.obs.TerminateRunning(); err != nil {
			t.Errorf("terminate escaped processes: %v", err)
		}
		if err := c.obs.Close(); err != nil {
			t.Errorf("release held handles: %v", err)
		}
	})
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), nestedUnitName); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *nestedManagerCase) liveProc(t *testing.T) runtime.Process {
	t.Helper()
	c.m.mu.Lock()
	defer c.m.mu.Unlock()
	p := c.m.procOfLocked(nestedUnitName)
	if p == nil {
		t.Fatal("manager owns no process for the nested unit")
	}
	return p
}

// ready waits for READY of gen and checks both layers. Unit-job membership is
// queried by PID while every held handle is unsignaled, so the PID cannot
// have been reused.
func (c *nestedManagerCase) ready(t *testing.T, gen int) (*nestedjob.Report, []*nestedjob.Held) {
	t.Helper()
	_, held, err := c.obs.Ready(gen)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.obs.Report(gen)
	if err != nil {
		t.Fatal(err)
	}
	if err := nestedjob.CheckTree(r, c.cfg.LaunchMode); err != nil {
		t.Fatal(err)
	}
	proc := c.liveProc(t)
	if proc.PID() != int(held[0].ID.PID) {
		t.Fatalf("manager main pid %d, tree MAIN %d", proc.PID(), held[0].ID.PID)
	}
	for _, h := range held {
		in, err := proc.Job().Contains(int(h.ID.PID))
		if err != nil {
			t.Fatal(err)
		}
		if done, serr := h.Signaled(); serr != nil || done || !in {
			t.Fatalf("%s: unit job member=%t exited=%t err=%v", h.ID.Role, in, done, serr)
		}
	}
	st, err := c.m.Status(nestedUnitName)
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit.InvocationID == "" || held[0].ID.Invocation != st.Unit.InvocationID {
		t.Fatalf("MAIN invocation %q, unit invocation %q", held[0].ID.Invocation, st.Unit.InvocationID)
	}
	return r, held
}

func requireDrained(t *testing.T, held []*nestedjob.Held, when string) {
	t.Helper()
	running, err := nestedjob.Unsignaled(held)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range running {
		late := nestedjob.WaitSignaled([]*nestedjob.Held{h}, 15*time.Second)
		t.Errorf("%s pid %d still running %s (after 15s more: %v)", h.ID.Role, h.ID.PID, when, late)
	}
}

// staysDown requires that no workload is launched again within window.
func (c *nestedManagerCase) staysDown(t *testing.T, window time.Duration) {
	t.Helper()
	time.Sleep(window)
	mains, _ := c.launch.state()
	c.m.mu.Lock()
	proc, st := c.m.procOfLocked(nestedUnitName), c.m.stateOfLocked(nestedUnitName)
	c.m.mu.Unlock()
	if mains != 1 || proc != nil || st == core.Active || st == core.Activating {
		t.Fatalf("explicit stop did not stay down: launches=%d state=%s owned=%t", mains, st, proc != nil)
	}
}

func eachNestedMode(t *testing.T, f func(t *testing.T, mode string)) {
	for _, mode := range nestedjob.LaunchModes {
		t.Run(mode, func(t *testing.T) { f(t, mode) })
	}
}

// N03: one cooperative ExecStop for the captured main removes both jobs.
func TestWindowsNestedJobCooperativeStop(t *testing.T) {
	eachNestedMode(t, func(t *testing.T, mode string) {
		base := t.TempDir()
		cfg := nestedCaseConfig(t, base, mode)
		stop := &nestedjob.StopConfig{CaseDir: cfg.CaseDir, Behavior: nestedjob.StopCooperative}
		c := startNestedManager(t, &nestedLauncher{}, cfg, base, "", "Restart=always\nRestartSec=1s\nTimeoutStopSec=5s\n", stop)
		_, held := c.ready(t, 1)
		if _, err := c.m.Stop(nestedUnitName); err != nil {
			t.Fatal(err)
		}
		requireDrained(t, held, "when the cooperative stop returned")
		helpers, err := c.obs.StopHelpers(1)
		if err != nil {
			t.Fatal(err)
		}
		if len(helpers) != 1 {
			t.Fatalf("stop helper ran %d times", len(helpers))
		}
		h := helpers[0]
		if !h.MainMatched || !h.MainExited || h.MainPID != held[0].ID.PID || h.Failure != nil {
			t.Fatalf("helper record %+v", h)
		}
		if h.Identity.Invocation != held[0].ID.Invocation+"-stop" {
			t.Fatalf("helper invocation %q for main %q", h.Identity.Invocation, held[0].ID.Invocation)
		}
		waitCond(t, func() bool {
			s := nestedjob.CheckGone(h.Identity).State
			return s == nestedjob.PreviousGone || s == nestedjob.PreviousExited || s == nestedjob.PreviousReused
		})
		r, err := c.obs.Report(1)
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{nestedjob.EventStopRequested, nestedjob.EventInnerClosed, nestedjob.EventStopComplete} {
			if r.Find(kind) == nil {
				t.Fatalf("report lacks %s", kind)
			}
		}
		c.staysDown(t, 3*time.Second)
	})
}

// N04: a hung helper and an ignored request fall back to forced cleanup of
// both jobs within the stop budget.
func TestWindowsNestedJobForcedStop(t *testing.T) {
	eachNestedMode(t, func(t *testing.T, mode string) {
		base := t.TempDir()
		cfg := nestedCaseConfig(t, base, mode)
		cfg.OnStop = nestedjob.OnStopIgnore
		stop := &nestedjob.StopConfig{CaseDir: cfg.CaseDir, Behavior: nestedjob.StopHang}
		c := startNestedManager(t, &nestedLauncher{}, cfg, base, "", "Restart=always\nRestartSec=1s\nTimeoutStopSec=5s\n", stop)
		_, held := c.ready(t, 1)
		started := time.Now()
		stopped := make(chan error, 1)
		go func() { _, err := c.m.Stop(nestedUnitName); stopped <- err }()
		var helper *nestedjob.Held
		waitCond(t, func() bool {
			hs, err := c.obs.StopHelpers(1)
			if err != nil || len(hs) != 1 {
				return false
			}
			if helper, err = c.obs.Hold(hs[0].Identity, 0); err != nil {
				t.Fatal(err)
			}
			return true
		})
		if _, err := c.obs.WaitReport(1, "ignored stop", func(r *nestedjob.Report) bool { return r.Find(nestedjob.EventStopIgnored) != nil }); err != nil {
			t.Fatal(err)
		}
		var err error
		select {
		case err = <-stopped:
		case <-time.After(20 * time.Second):
			t.Fatal("forced stop exceeded TimeoutStopSec plus observation slack")
		}
		elapsed := time.Since(started)
		if err == nil {
			t.Fatal("hung helper reported a successful cooperative stop")
		}
		if elapsed < 3*time.Second {
			t.Fatalf("stop returned after %s, before the cooperative phase expired", elapsed)
		}
		t.Logf("forced stop after %s: %v", elapsed, err)
		requireDrained(t, held, "when the forced stop returned")
		if err := nestedjob.WaitSignaled([]*nestedjob.Held{helper}, 15*time.Second); err != nil {
			t.Fatalf("hung helper survived forced cleanup: %v", err)
		}
		waitCond(t, func() bool { c.m.mu.Lock(); defer c.m.mu.Unlock(); return len(c.m.stopHelpers) == 0 })
		st, err := c.m.Status(nestedUnitName)
		if err != nil {
			t.Fatal(err)
		}
		if st.Unit.TerminationUncertain {
			t.Fatalf("cleanup remains uncertain: %v", st.Unit.PendingCleanup)
		}
		c.staysDown(t, 3*time.Second)
	})
}

// N05: MAIN's death closes its sole inner handle; the replacement starts only
// after every old process has exited and gets a new invocation.
func TestWindowsNestedJobMainCrashRestart(t *testing.T) {
	eachNestedMode(t, func(t *testing.T, mode string) {
		base := t.TempDir()
		cfg := nestedCaseConfig(t, base, mode)
		cfg.Generation = 0
		launch := &nestedLauncher{}
		c := startNestedManager(t, launch, cfg, base, "", "Restart=always\nRestartSec=1s\n", nil)
		_, held := c.ready(t, 1)
		launch.expectDrained(held)
		if err := nestedjob.Terminate(held[0]); err != nil {
			t.Fatal(err)
		}
		if err := nestedjob.WaitSignaled(held, 10*time.Second); err != nil {
			t.Fatalf("old tree after MAIN crash: %v", err)
		}
		if _, err := c.obs.WaitManifest(2, nestedjob.StageTree); err != nil {
			t.Fatal(err)
		}
		r, next := c.ready(t, 2)
		mains, violations := launch.state()
		if mains != 2 || len(violations) != 0 {
			t.Fatalf("replacement launches=%d violations=%v", mains, violations)
		}
		previous := r.Find(nestedjob.EventPrevious)
		if len(previous.Previous) != 4 {
			t.Fatalf("replacement checked %d previous processes", len(previous.Previous))
		}
		for _, p := range previous.Previous {
			if p.State != nestedjob.PreviousExited && p.State != nestedjob.PreviousGone && p.State != nestedjob.PreviousReused {
				t.Fatalf("previous %s state %s at replacement entry", p.Role, p.State)
			}
		}
		if next[0].ID.Invocation == held[0].ID.Invocation {
			t.Fatal("replacement reused the old invocation ID")
		}
		if _, err := c.m.Stop(nestedUnitName); err != nil {
			t.Fatal(err)
		}
		requireDrained(t, next, "when the replacement stop returned")
	})
}

// N07, owner-lane part: the process owning the daemon and unit jobs dies.
// Genuine broker/user-manager recovery belongs to the installed-daemon lane.
func TestWindowsNestedJobOwnerCrash(t *testing.T) {
	eachNestedMode(t, func(t *testing.T, mode string) {
		base := t.TempDir()
		cfg := nestedCaseConfig(t, base, mode)
		writeNestedUnit(t, base, cfg, "", "Restart=no\n", nil)
		exe, err := filepath.Abs(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, winunitdHelperArgPrefix+"nested-owner", base)
		cmd.Env = nestedHelperEnv()
		cmd.SysProcAttr = &windows.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		waited := make(chan error, 1)
		go func() { waited <- cmd.Wait() }()
		obs := nestedjob.NewObserver(cfg.CaseDir)
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			<-waited
			if err := obs.TerminateRunning(); err != nil {
				t.Errorf("terminate survivors: %v", err)
			}
			if err := obs.Close(); err != nil {
				t.Error(err)
			}
			if t.Failed() {
				t.Logf("owner output:\n%s", out.String())
			}
		})
		_, held, err := obs.Ready(1)
		if err != nil {
			t.Fatal(err)
		}
		r, err := obs.Report(1)
		if err != nil {
			t.Fatal(err)
		}
		if err := nestedjob.CheckTree(r, mode); err != nil {
			t.Fatal(err)
		}
		created, err := nestedjob.CreationTime(processHandle(t, cmd.Process.Pid))
		if err != nil {
			t.Fatal(err)
		}
		owner, err := obs.Hold(nestedjob.Identity{Role: "owner", PID: uint32(cmd.Process.Pid), Created: created, Generation: 1}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := nestedjob.Terminate(owner); err != nil {
			t.Fatal(err)
		}
		if err := nestedjob.WaitSignaled([]*nestedjob.Held{owner}, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		if err := nestedjob.WaitSignaled(held, 10*time.Second); err != nil {
			t.Fatalf("nested tree survived its owner: %v", err)
		}
	})
}

// processHandle opens pid for a creation-time query; the caller's exec.Cmd
// keeps its own handle, so the PID cannot be reused meanwhile.
func processHandle(t *testing.T, pid int) windows.Handle {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(h) })
	return h
}

func nestedHelperEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "WINUNITD_JOB_") {
			env = append(env, e)
		}
	}
	return env
}

// runNestedOwner is the owner process for TestWindowsNestedJobOwnerCrash: it
// owns a kill-on-close daemon job, a manager and the nested unit, then waits
// to be terminated without any cleanup.
func runNestedOwner(base string) error {
	job, err := runtime.OpenDaemonJob()
	if err != nil {
		return err
	}
	if err := job.AssignSelf(); err != nil {
		return err
	}
	m, err := New(Config{BaseDir: base, Daemon: job})
	if err != nil {
		return err
	}
	if _, err := m.Reload(); err != nil {
		return err
	}
	if _, err := m.Start(context.Background(), nestedUnitName); err != nil {
		return err
	}
	for {
		time.Sleep(time.Hour)
	}
}

// N08: aggregate commitments of the inner tree hit the unit job's MemoryMax.
func TestWindowsNestedJobMemoryMax(t *testing.T) {
	eachNestedMode(t, func(t *testing.T, mode string) {
		base := t.TempDir()
		cfg := nestedCaseConfig(t, base, mode)
		cfg.Work = nestedjob.WorkCommit
		c := startNestedManager(t, &nestedLauncher{}, cfg, base, "", "Restart=no\nMemoryMax=256M\n", nil)
		r, held := c.ready(t, 1)
		var baseline uint64
		for _, m := range r.Find(nestedjob.EventTree).Tree {
			baseline += m.PrivateBytes
		}
		t.Logf("baseline private bytes of the four processes: %d", baseline)
		if baseline >= 128<<20 {
			t.Fatalf("fixture baseline %d bytes leaves too little of MemoryMax=256M; review the threshold before running", baseline)
		}
		job := c.liveProc(t).Job()
		limits, err := job.QueryLimits()
		if err != nil {
			t.Fatal(err)
		}
		if limits.JobMemory != 256<<20 || limits.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY == 0 {
			t.Fatalf("unit job memory limit %d flags %#x", limits.JobMemory, limits.LimitFlags)
		}
		for _, role := range []string{nestedjob.RoleEngine, nestedjob.RoleG1, nestedjob.RoleG2} {
			if _, err := c.obs.Command(1, role, nestedjob.Command{Verb: nestedjob.VerbStartWork}); err != nil {
				t.Fatal(err)
			}
		}
		var peak uint64
		deadline := time.Now().Add(30 * time.Second)
		for {
			if got, err := job.QueryLimits(); err == nil && got.PeakJobMemory > peak {
				peak = got.PeakJobMemory
			}
			st, err := c.m.Status(nestedUnitName)
			if err != nil {
				t.Fatal(err)
			}
			if st.Unit.ActiveState == core.Failed.String() {
				if st.Unit.Reason != core.ReasonResourceLimit {
					t.Fatalf("failed with reason %q error %q", st.Unit.Reason, st.Unit.Error)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no resource-limit failure: state=%s peak=%d", st.Unit.ActiveState, peak)
			}
			time.Sleep(20 * time.Millisecond)
		}
		requireDrained(t, held, "after the resource-limit failure")
		var committed uint64
		for _, role := range []string{nestedjob.RoleEngine, nestedjob.RoleG1, nestedjob.RoleG2} {
			if w, err := c.obs.Work(1, role); err == nil {
				committed += w.CommittedBytes
				t.Logf("%s committed %d: %s", role, w.CommittedBytes, w.Failure.Error())
			}
		}
		t.Logf("peak job memory %d, recorded work commitments %d", peak, committed)
		if peak > 256<<20 || committed >= 3*nestedjob.CommitPerProcess {
			t.Fatalf("commitments exceeded MemoryMax: peak=%d work=%d", peak, committed)
		}
	})
}

// N09: format 1 CPUQuota applies to the unit job while the inner job exists.
func TestWindowsNestedJobCPUQuota(t *testing.T) {
	testNestedCPUQuota(t, "", "CPUQuota=25%\n", 25, 0)
}

// N10: format 2 WindowsCPUQuota, reported under its own status name.
func TestWindowsNestedJobWindowsCPUQuota(t *testing.T) {
	testNestedCPUQuota(t, "FormatVersion=2\n", "WindowsCPUQuota=25%\n", 0, 25)
}

// With WINUNITD_NATIVE_NESTED_METER=1 the quota is also measured against an
// uncapped control; otherwise only the native settings are checked.
func testNestedCPUQuota(t *testing.T, unitLines, quota string, legacy, native uint32) {
	meter := os.Getenv("WINUNITD_NATIVE_NESTED_METER") == "1"
	eachNestedMode(t, func(t *testing.T, mode string) {
		start := func(t *testing.T, serviceLines string) (*nestedManagerCase, []*nestedjob.Held) {
			base := t.TempDir()
			cfg := nestedCaseConfig(t, base, mode)
			cfg.Work = nestedjob.WorkCPU
			c := startNestedManager(t, &nestedLauncher{}, cfg, base, unitLines, "Restart=no\n"+serviceLines, nil)
			_, held := c.ready(t, 1)
			return c, held
		}
		var control float64
		if meter {
			c, held := start(t, "")
			control = nestedCPUShare(t, c, held)
			t.Logf("uncapped control: %.1f%% of %d processors", control*100, goruntime.NumCPU())
			if _, err := c.m.Stop(nestedUnitName); err != nil {
				t.Fatal(err)
			}
			requireDrained(t, held, "after the control")
		}
		c, held := start(t, quota)
		got, err := c.liveProc(t).Job().QueryLimits()
		if err != nil {
			t.Fatal(err)
		}
		if got.CPURate != unit.WindowsCPURate(25) || got.CPUControlFlags&runtime.JobCPURateHardCap == 0 {
			t.Fatalf("unit job CPU rate %d flags %#x", got.CPURate, got.CPUControlFlags)
		}
		st, err := c.m.Status(nestedUnitName)
		if err != nil {
			t.Fatal(err)
		}
		if st.Unit.CPUQuota != legacy || st.Unit.WindowsCPUQuota != native {
			t.Fatalf("status cpuQuota=%d windowsCPUQuota=%d, want %d/%d", st.Unit.CPUQuota, st.Unit.WindowsCPUQuota, legacy, native)
		}
		capped := -1.0
		if meter {
			capped = nestedCPUShare(t, c, held)
			t.Logf("capped: %.1f%% of %d processors (control %.1f%%)", capped*100, goruntime.NumCPU(), control*100)
		} else {
			t.Log("measurement not requested; native settings only")
		}
		if _, err := c.m.Stop(nestedUnitName); err != nil {
			t.Fatal(err)
		}
		requireDrained(t, held, "after the quota case")
		if !meter {
			return
		}
		if control < 0.60 {
			t.Skipf("INCONCLUSIVE: uncapped control reached only %.1f%%", control*100)
		}
		if capped > 0.35 {
			t.Fatalf("busy inner tree used %.1f%% under a 25%% quota (bound 35%%)", capped*100)
		}
	})
}

// nestedCPUShare starts CPU work in ENGINE, G1 and G2 and measures their
// combined share of all processors over 30 seconds after a 5-second warm-up.
func nestedCPUShare(t *testing.T, c *nestedManagerCase, held []*nestedjob.Held) float64 {
	t.Helper()
	for _, role := range []string{nestedjob.RoleEngine, nestedjob.RoleG1, nestedjob.RoleG2} {
		if _, err := c.obs.Command(1, role, nestedjob.Command{Verb: nestedjob.VerbStartWork}); err != nil {
			t.Fatal(err)
		}
	}
	sample := func() (uint64, time.Time) {
		var total uint64
		for _, h := range held[1:] {
			v, err := nestedjob.CPUTime(h.Handle)
			if err != nil {
				t.Fatal(err)
			}
			total += v
		}
		return total, time.Now()
	}
	time.Sleep(5 * time.Second)
	cpu0, wall0 := sample()
	time.Sleep(30 * time.Second)
	cpu1, wall1 := sample()
	if running, err := nestedjob.Unsignaled(held); err != nil || len(running) != len(held) {
		t.Fatalf("tree changed during measurement: %d running, %v", len(running), err)
	}
	busy := time.Duration(cpu1-cpu0) * 100
	return busy.Seconds() / (wall1.Sub(wall0).Seconds() * float64(goruntime.NumCPU()))
}

// N16: a test-only seam fails the first cleanup of a real nested tree.
func TestWindowsNestedJobCleanupUncertainty(t *testing.T) {
	eachNestedMode(t, func(t *testing.T, mode string) {
		for _, lie := range []bool{false, true} {
			name := "failure"
			if lie {
				name = "success-with-live-process"
			}
			t.Run(name, func(t *testing.T) {
				base := t.TempDir()
				cfg := nestedCaseConfig(t, base, mode)
				launch := &nestedLauncher{seam: true, lie: lie}
				c := startNestedManager(t, launch, cfg, base, "", "Restart=no\n", nil)
				_, held := c.ready(t, 1)
				launch.mu.Lock()
				seam := launch.failing
				launch.mu.Unlock()
				if _, err := c.m.Stop(nestedUnitName); err == nil {
					t.Error("unconfirmed cleanup reported success")
				}
				c.m.mu.Lock()
				owned := c.m.procOfLocked(nestedUnitName) == runtime.Process(seam)
				c.m.mu.Unlock()
				if !owned {
					t.Fatal("failed cleanup released ownership")
				}
				if running, err := nestedjob.Unsignaled(held); err != nil || len(running) != len(held) {
					t.Fatalf("seam did not retain the real tree: %d running, %v", len(running), err)
				}
				st, err := c.m.Status(nestedUnitName)
				if err != nil {
					t.Fatal(err)
				}
				if !st.Unit.TerminationUncertain {
					t.Error("status does not report uncertain termination")
				}
				if _, err := c.m.Start(context.Background(), nestedUnitName); err == nil {
					t.Error("replacement admitted while cleanup is unconfirmed")
				}
				if mains, _ := launch.state(); mains != 1 {
					t.Fatalf("replacement launched (%d launches)", mains)
				}
				seam.fail.Store(false)
				if _, err := c.m.Stop(nestedUnitName); err != nil {
					t.Fatalf("cleanup retry: %v", err)
				}
				requireDrained(t, held, "when the cleanup retry returned")
			})
		}
	})
}
