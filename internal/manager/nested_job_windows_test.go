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

// Workload-created nested jobs (#265), native-owner lane with a real Manager,
// the Windows launcher, real jobs and ExecStop. Subtests are mode then
// identity, matching the case matrix's selectors. See
// nested_job_owner_windows_test.go for the SYSTEM and headless owners.

const nestedUnitName = "nested.service"

// nestedLauncher wraps the real launcher. Each workload launch records which
// previous-generation processes were still running at that moment, without
// waiting or delaying the launch: the replacement-acceptance boundary in this
// lane. With seam set, the first workload is wrapped in failedStopProcess so
// its first cleanup fails.
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
	l.mu.Lock()
	defer l.mu.Unlock()
	if helper || !l.seam || p == nil || l.failing != nil {
		return p, err
	}
	f := &failedStopProcess{Process: p, lie: l.lie}
	f.fail.Store(true)
	l.failing = f
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

// release lets a seam-wrapped workload be cleaned up normally.
func (l *nestedLauncher) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failing != nil {
		l.failing.fail.Store(false)
	}
}

type nestedManagerCase struct {
	owner    managerOwner
	obs      *nestedjob.Observer
	cfg      nestedjob.MainConfig
	rec      *nestedjob.Record
	identity string
	drained  bool
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

// nestedBase returns a manager base directory with a case directory inside.
// With the evidence root configured, both are kept. With sid set, the base
// is protected for SYSTEM, Administrators and that account only.
func nestedBase(t *testing.T, mode, sid string) (string, nestedjob.MainConfig) {
	t.Helper()
	root := t.TempDir()
	if r := os.Getenv(nestedjob.EnvCaseRoot); r != "" {
		if !filepath.IsAbs(r) {
			t.Fatal(nestedjob.EnvCaseRoot + " must be absolute")
		}
		root = r
	}
	base, err := os.MkdirTemp(root, "manager-")
	if err != nil {
		t.Fatal(err)
	}
	if sid != "" {
		if err := nestedjob.ProtectDirectory(base, sid); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(base, "case")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return base, nestedjob.MainConfig{LaunchMode: mode, CaseDir: dir, Generation: 1, Work: nestedjob.WorkIdle, OnStop: nestedjob.OnStopCooperative}
}

// nestedRecord writes the scenario's result record after everything else,
// including the case's own cleanup.
func nestedRecord(t *testing.T, caseID, mode, identity string) *nestedjob.Record {
	t.Helper()
	rec, err := nestedjob.NewRecord(caseID, mode, identity, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		result := nestedjob.ResultPass
		switch {
		case t.Failed():
			result = nestedjob.ResultFail
		case t.Skipped():
			result = nestedjob.ResultSkip
		}
		if err := rec.Finish(result); err != nil {
			t.Errorf("result record: %v", err)
		}
	})
	return rec
}

// eachManagerLane runs f per mode and identity with the scenario's record.
func eachManagerLane(t *testing.T, caseID string, identities []string, f func(t *testing.T, mode, identity string, rec *nestedjob.Record)) {
	for _, mode := range nestedjob.LaunchModes {
		t.Run(mode, func(t *testing.T) {
			for _, identity := range identities {
				t.Run(identity, func(t *testing.T) { f(t, mode, identity, nestedRecord(t, caseID, mode, identity)) })
			}
		})
	}
}

var nestedManagerIdentities = []string{nestedjob.IdentitySystem, nestedjob.IdentityHeadless}

// nestedCase describes one unit to render and start.
type nestedCase struct {
	mode         string
	identity     string
	unitLines    string
	serviceLines string
	stop         string // ExecStop helper behavior, or none
	configure    func(*nestedjob.MainConfig)
	launch       *nestedLauncher // SYSTEM lane only; nil uses a plain observer
}

// startNestedCase selects the lane's owner, renders nested.service and
// starts it. Cleanup stops the unit and closes the owner, terminates
// anything that escaped and releases held handles; the record's cleanup is
// confirmed only when every step succeeded.
func startNestedCase(t *testing.T, rec *nestedjob.Record, nc nestedCase) *nestedManagerCase {
	t.Helper()
	launch := nc.launch
	if launch == nil {
		launch = &nestedLauncher{}
	}
	owner, base, cfg := newManagerOwner(t, nc.identity, nc.mode, launch)
	if nc.configure != nil {
		nc.configure(&cfg)
	}
	var stop *nestedjob.StopConfig
	if nc.stop != "" {
		stop = &nestedjob.StopConfig{CaseDir: cfg.CaseDir, Behavior: nc.stop}
	}
	writeNestedUnit(t, base, cfg, nc.unitLines, nc.serviceLines, stop)
	c := &nestedManagerCase{owner: owner, obs: nestedjob.NewObserver(cfg.CaseDir), cfg: cfg, rec: rec, identity: nc.identity}
	rec.Token = owner.token()
	t.Cleanup(func() {
		closeErr := owner.close()
		escaped := c.obs.TerminateRunning()
		releaseErr := c.obs.Close()
		for _, err := range []error{closeErr, escaped, releaseErr} {
			if err != nil {
				t.Error(err)
			}
		}
		rec.Cleanup(c.drained && closeErr == nil && escaped == nil && releaseErr == nil)
	})
	owner.start(t)
	return c
}

// ready waits for READY of gen and checks both layers and the token. Unit
// job membership is queried while every held handle is unsignaled, so the
// PID cannot have been reused.
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
	v := c.owner.inspect(t)
	if v.MainPID != int(held[0].ID.PID) {
		t.Fatalf("manager main pid %d, tree MAIN %d", v.MainPID, held[0].ID.PID)
	}
	if v.InvocationID == "" || held[0].ID.Invocation != v.InvocationID {
		t.Fatalf("MAIN invocation %q, unit invocation %q", held[0].ID.Invocation, v.InvocationID)
	}
	tok := c.owner.token()
	for _, h := range held {
		if done, err := h.Signaled(); err != nil || done {
			t.Fatalf("%s is not running at READY: %v", h.ID.Role, err)
		}
		if !c.owner.inUnitJob(t, h) {
			t.Fatalf("%s is not in the unit job", h.ID.Role)
		}
		if h.ID.SID != tok.SID || h.ID.Session != tok.Session || h.ID.Elevated != tok.Elevated {
			t.Fatalf("%s runs as %s/%d/%t, not the owner's %s/%d/%t", h.ID.Role, h.ID.SID, h.ID.Session, h.ID.Elevated, tok.SID, tok.Session, tok.Elevated)
		}
	}
	if got := nestedjob.IdentityOf(tok); got != c.identity {
		if c.identity == nestedjob.IdentityHeadless {
			t.Fatalf("owner token supports %q, not headless", got)
		}
		t.Log("owner is not SYSTEM: a generic regression run, not SYSTEM evidence")
		c.rec.Note("owner token is not SYSTEM")
	}
	return r, held
}

// requireDrained requires every held process to be signaled now; an empty
// job list alone is not exit evidence.
func (c *nestedManagerCase) requireDrained(t *testing.T, held []*nestedjob.Held, when string) {
	t.Helper()
	running, err := nestedjob.Unsignaled(held)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range running {
		late := nestedjob.WaitSignaled([]*nestedjob.Held{h}, 15*time.Second)
		t.Errorf("%s pid %d still running %s (after 15s more: %v)", h.ID.Role, h.ID.PID, when, late)
	}
	c.drained = len(running) == 0
}

// staysDown requires that no workload is launched again within window.
func (c *nestedManagerCase) staysDown(t *testing.T, window time.Duration) {
	t.Helper()
	time.Sleep(window)
	v := c.owner.inspect(t)
	if v.Launches != 1 || v.MainPID != 0 || v.ActiveState == core.Active.String() || v.ActiveState == core.Activating.String() {
		t.Fatalf("explicit stop did not stay down: launches=%d state=%s main=%d", v.Launches, v.ActiveState, v.MainPID)
	}
}

// waitView polls the owner until accept holds or timeout passes.
func (c *nestedManagerCase) waitView(t *testing.T, what string, timeout time.Duration, accept func(nestedjob.ManagerView) bool) nestedjob.ManagerView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		v := c.owner.inspect(t)
		if accept(v) {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %+v", what, v)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// N03: one cooperative ExecStop for the captured main removes both jobs.
func TestWindowsNestedJobCooperativeStop(t *testing.T) {
	eachManagerLane(t, "N03", nestedManagerIdentities, func(t *testing.T, mode, identity string, rec *nestedjob.Record) {
		c := startNestedCase(t, rec, nestedCase{mode: mode, identity: identity,
			serviceLines: "Restart=always\nRestartSec=1s\nTimeoutStopSec=5s\n", stop: nestedjob.StopCooperative})
		_, held := c.ready(t, 1)
		if _, err := c.owner.stopUnit(t); err != nil {
			t.Fatal(err)
		}
		c.requireDrained(t, held, "when the cooperative stop returned")
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
		if h.Identity.Invocation != held[0].ID.Invocation+"-stop" || h.Identity.SID != held[0].ID.SID {
			t.Fatalf("helper invocation %q (%s) for main %q (%s)", h.Identity.Invocation, h.Identity.SID, held[0].ID.Invocation, held[0].ID.SID)
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
	eachManagerLane(t, "N04", nestedManagerIdentities, func(t *testing.T, mode, identity string, rec *nestedjob.Record) {
		c := startNestedCase(t, rec, nestedCase{mode: mode, identity: identity,
			serviceLines: "Restart=always\nRestartSec=1s\nTimeoutStopSec=5s\n", stop: nestedjob.StopHang,
			configure: func(cfg *nestedjob.MainConfig) { cfg.OnStop = nestedjob.OnStopIgnore }})
		_, held := c.ready(t, 1)
		result := c.owner.stopAsync(t)
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
		elapsed, err, waitErr := result(20 * time.Second)
		if waitErr != nil {
			t.Fatalf("forced stop exceeded TimeoutStopSec plus observation slack: %v", waitErr)
		}
		if err == nil {
			t.Fatal("hung helper reported a successful cooperative stop")
		}
		if elapsed < 3*time.Second {
			t.Fatalf("stop returned after %s, before the cooperative phase expired", elapsed)
		}
		t.Logf("forced stop after %s: %v", elapsed, err)
		rec.Note(fmt.Sprintf("forced stop after %s", elapsed.Round(time.Millisecond)))
		c.requireDrained(t, held, "when the forced stop returned")
		if err := nestedjob.WaitSignaled([]*nestedjob.Held{helper}, 15*time.Second); err != nil {
			t.Fatalf("hung helper survived forced cleanup: %v", err)
		}
		v := c.waitView(t, "stop helpers released", 30*time.Second, func(v nestedjob.ManagerView) bool { return v.StopHelpers == 0 })
		if v.TerminationUncertain {
			t.Fatalf("cleanup remains uncertain: %+v", v)
		}
		c.staysDown(t, 3*time.Second)
	})
}

// N05: MAIN's death closes its sole inner handle; the replacement starts only
// after every old process has exited and gets a new invocation. The check at
// the replacement launch observes and never waits or delays the launch.
func TestWindowsNestedJobMainCrashRestart(t *testing.T) {
	eachManagerLane(t, "N05", nestedManagerIdentities, func(t *testing.T, mode, identity string, rec *nestedjob.Record) {
		c := startNestedCase(t, rec, nestedCase{mode: mode, identity: identity,
			serviceLines: "Restart=always\nRestartSec=1s\n",
			configure:    func(cfg *nestedjob.MainConfig) { cfg.Generation = 0 }})
		_, held := c.ready(t, 1)
		c.owner.expectDrained(t, held)
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
		v := c.owner.inspect(t)
		if v.Launches != 2 || len(v.Violations) != 0 {
			t.Fatalf("replacement launches=%d violations=%v", v.Launches, v.Violations)
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
		if _, err := c.owner.stopUnit(t); err != nil {
			t.Fatal(err)
		}
		c.requireDrained(t, next, "when the replacement stop returned")
	})
}

// Supplementary, not a primary matrix execution: the process owning the
// daemon and unit jobs dies. Installed-daemon recovery is N06/N07's lane.
func TestWindowsNestedJobOwnerCrash(t *testing.T) {
	for _, mode := range nestedjob.LaunchModes {
		t.Run(mode, func(t *testing.T) {
			base, cfg := nestedBase(t, mode, "")
			writeNestedUnit(t, base, cfg, "", "Restart=no\n", nil)
			exe, err := filepath.Abs(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, winunitdHelperArgPrefix+"nested-owner-crash", base)
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
			drained := false
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				<-waited
				escaped := obs.TerminateRunning()
				if escaped != nil {
					t.Errorf("terminate survivors: %v", escaped)
				}
				if err := obs.Close(); err != nil {
					t.Error(err)
				}
				if t.Failed() {
					t.Logf("owner output:\n%s", out.String())
				}
				writeSupplementary(t, "owner-crash/"+mode+"/system", drained && escaped == nil)
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
			owner, err := obs.Hold(nestedjob.Identity{Role: nestedjob.RoleOwner, PID: uint32(cmd.Process.Pid), Created: created, Generation: 1}, 0)
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
			drained = true
		})
	}
}

// writeSupplementary records a supplementary regression under its own key.
func writeSupplementary(t *testing.T, key string, cleanup bool) {
	t.Helper()
	dir := os.Getenv(nestedjob.EnvResults)
	if dir == "" {
		return
	}
	result := nestedjob.ResultPass
	if t.Failed() {
		result = nestedjob.ResultFail
	}
	tok, _ := nestedjob.CurrentTokenContext()
	r := nestedjob.Result{Schema: nestedjob.ResultSchema, Key: key, Kind: nestedjob.KindSupplementary, Result: result,
		Source: os.Getenv(nestedjob.EnvSource), Matrix: nestedjob.MatrixHash(), Token: tok, CleanupConfirmed: cleanup}
	if err := nestedjob.WriteResult(dir, r); err != nil {
		t.Errorf("supplementary record: %v", err)
	}
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
	eachManagerLane(t, "N08", nestedManagerIdentities, func(t *testing.T, mode, identity string, rec *nestedjob.Record) {
		c := startNestedCase(t, rec, nestedCase{mode: mode, identity: identity,
			serviceLines: "Restart=no\nMemoryMax=256M\n",
			configure:    func(cfg *nestedjob.MainConfig) { cfg.Work = nestedjob.WorkCommit }})
		r, held := c.ready(t, 1)
		var baseline uint64
		for _, m := range r.Find(nestedjob.EventTree).Tree {
			baseline += m.PrivateBytes
		}
		t.Logf("baseline private bytes of the four processes: %d", baseline)
		if baseline >= 128<<20 {
			t.Fatalf("fixture baseline %d bytes leaves too little of MemoryMax=256M; review the threshold before running", baseline)
		}
		v := c.owner.inspect(t)
		if !v.HasJob || v.JobMemory != 256<<20 || v.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY == 0 {
			t.Fatalf("unit job memory limit %d flags %#x", v.JobMemory, v.LimitFlags)
		}
		for _, role := range []string{nestedjob.RoleEngine, nestedjob.RoleG1, nestedjob.RoleG2} {
			if _, err := c.obs.Command(1, role, nestedjob.Command{Verb: nestedjob.VerbStartWork}); err != nil {
				t.Fatal(err)
			}
		}
		var peak uint64
		v = c.waitView(t, "a resource-limit failure", 30*time.Second, func(v nestedjob.ManagerView) bool {
			peak = max(peak, v.PeakJobMemory)
			return v.ActiveState == core.Failed.String()
		})
		if v.Reason != core.ReasonResourceLimit {
			t.Fatalf("failed with reason %q error %q", v.Reason, v.Error)
		}
		c.requireDrained(t, held, "after the resource-limit failure")
		var committed uint64
		for _, role := range []string{nestedjob.RoleEngine, nestedjob.RoleG1, nestedjob.RoleG2} {
			if w, err := c.obs.Work(1, role); err == nil {
				committed += w.CommittedBytes
				t.Logf("%s committed %d: %s", role, w.CommittedBytes, w.Failure.Error())
			}
		}
		t.Logf("peak job memory %d, recorded work commitments %d", peak, committed)
		rec.Note(fmt.Sprintf("baseline %d peak %d committed %d", baseline, peak, committed))
		if peak > 256<<20 || committed >= 3*nestedjob.CommitPerProcess {
			t.Fatalf("commitments exceeded MemoryMax: peak=%d work=%d", peak, committed)
		}
	})
}

// N09: format 1 CPUQuota applies to the unit job while the inner job exists.
func TestWindowsNestedJobCPUQuota(t *testing.T) {
	testNestedCPUQuota(t, "N09", "", "CPUQuota=25%\n", 25, 0)
}

// N10: format 2 WindowsCPUQuota, reported under its own status name.
func TestWindowsNestedJobWindowsCPUQuota(t *testing.T) {
	testNestedCPUQuota(t, "N10", "FormatVersion=2\n", "WindowsCPUQuota=25%\n", 0, 25)
}

// With WINUNITD_NATIVE_NESTED_METER=1 the quota is also measured against an
// uncapped control with the same mode, token and processors, recorded as a
// separately keyed control; otherwise only the native settings are checked.
func testNestedCPUQuota(t *testing.T, caseID, unitLines, quota string, legacy, native uint32) {
	meter := os.Getenv(nestedjob.EnvMeter) == "1"
	eachManagerLane(t, caseID, nestedManagerIdentities, func(t *testing.T, mode, identity string, rec *nestedjob.Record) {
		start := func(t *testing.T, serviceLines string) (*nestedManagerCase, []*nestedjob.Held) {
			c := startNestedCase(t, rec, nestedCase{mode: mode, identity: identity, unitLines: unitLines,
				serviceLines: "Restart=no\n" + serviceLines,
				configure:    func(cfg *nestedjob.MainConfig) { cfg.Work = nestedjob.WorkCPU }})
			_, held := c.ready(t, 1)
			return c, held
		}
		var control float64
		if meter {
			c, held := start(t, "")
			control = nestedCPUShare(t, c, held)
			t.Logf("uncapped control: %.1f%% of %d processors", control*100, goruntime.NumCPU())
			if _, err := c.owner.stopUnit(t); err != nil {
				t.Fatal(err)
			}
			c.requireDrained(t, held, "after the control")
			result := nestedjob.ResultPass
			if control < 0.60 {
				result = nestedjob.ResultInconclusive
			}
			rec.Control("uncapped", result, fmt.Sprintf("%.1f%% of %d processors", control*100, goruntime.NumCPU()))
		}
		c, held := start(t, quota)
		v := c.owner.inspect(t)
		if !v.HasJob || v.CPURate != unit.WindowsCPURate(25) || v.CPUControlFlags&runtime.JobCPURateHardCap == 0 {
			t.Fatalf("unit job CPU rate %d flags %#x", v.CPURate, v.CPUControlFlags)
		}
		if v.CPUQuota != legacy || v.WindowsCPUQuota != native {
			t.Fatalf("status cpuQuota=%d windowsCPUQuota=%d, want %d/%d", v.CPUQuota, v.WindowsCPUQuota, legacy, native)
		}
		capped := -1.0
		if meter {
			capped = nestedCPUShare(t, c, held)
			t.Logf("capped: %.1f%% of %d processors (control %.1f%%)", capped*100, goruntime.NumCPU(), control*100)
			rec.Note(fmt.Sprintf("capped %.1f%% control %.1f%%", capped*100, control*100))
		} else {
			t.Log("measurement not requested; native settings only")
			rec.Note("native settings only; not metered")
		}
		if _, err := c.owner.stopUnit(t); err != nil {
			t.Fatal(err)
		}
		c.requireDrained(t, held, "after the quota case")
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

// N16: a test-only seam fails the first cleanup of a real nested tree. While
// it is held no replacement may launch or be admitted; a start that returns
// early must be a refusal, and a pending start is allowed. After release,
// every held process exits, cleanup is confirmed and a fresh invocation runs.
// This exercises state handling, not a reproduced kernel failure. SYSTEM
// lane only: the seam lives in this test process.
func TestWindowsNestedJobCleanupUncertainty(t *testing.T) {
	eachManagerLane(t, "N16", []string{nestedjob.IdentitySystem}, func(t *testing.T, mode, identity string, rec *nestedjob.Record) {
		for _, lie := range []bool{false, true} {
			name := "failure"
			if lie {
				name = "success-with-live-process"
			}
			t.Run(name, func(t *testing.T) {
				launch := &nestedLauncher{seam: true, lie: lie}
				c := startNestedCase(t, rec, nestedCase{mode: mode, identity: identity, serviceLines: "Restart=no\n", launch: launch,
					configure: func(cfg *nestedjob.MainConfig) { cfg.Generation = 0 }})
				m := c.owner.(*localManagerOwner).m
				_, held := c.ready(t, 1)
				launch.mu.Lock()
				seam := launch.failing
				launch.mu.Unlock()
				// Release in every outcome so teardown can drain the tree.
				t.Cleanup(launch.release)
				if _, err := m.Stop(nestedUnitName); err == nil {
					t.Error("unconfirmed cleanup reported success")
				}
				m.mu.Lock()
				owned := m.procOfLocked(nestedUnitName) == runtime.Process(seam)
				m.mu.Unlock()
				if !owned {
					t.Fatal("failed cleanup released ownership")
				}
				if running, err := nestedjob.Unsignaled(held); err != nil || len(running) != len(held) {
					t.Fatalf("seam did not retain the real tree: %d running, %v", len(running), err)
				}
				if v := c.owner.inspect(t); !v.TerminationUncertain {
					t.Error("status does not report uncertain termination")
				}
				launch.expectDrained(held)
				startResult := make(chan error, 1)
				go func() { _, err := m.Start(context.Background(), nestedUnitName); startResult <- err }()
				pending := false
				select {
				case err := <-startResult:
					if err == nil {
						t.Fatal("a start returned success while cleanup was unconfirmed")
					}
				case <-time.After(2 * time.Second):
					pending = true
					rec.Note("start stayed pending while cleanup was held")
				}
				if mains, _ := launch.state(); mains != 1 {
					t.Fatalf("replacement launched while cleanup was held (%d launches)", mains)
				}
				launch.release()
				if _, err := m.Stop(nestedUnitName); err != nil {
					t.Fatalf("cleanup retry: %v", err)
				}
				c.requireDrained(t, held, "when the cleanup retry returned")
				if pending {
					select {
					case <-startResult:
					case <-time.After(30 * time.Second):
						t.Fatal("the pending start never finished")
					}
				}
				if mains, _ := launch.state(); mains == 1 {
					if _, err := m.Start(context.Background(), nestedUnitName); err != nil {
						t.Fatalf("fresh invocation after confirmed cleanup: %v", err)
					}
				}
				_, next := c.ready(t, 2)
				mains, violations := launch.state()
				if mains != 2 || len(violations) != 0 {
					t.Fatalf("fresh invocation launches=%d violations=%v", mains, violations)
				}
				if next[0].ID.Invocation == held[0].ID.Invocation {
					t.Fatal("fresh invocation reused the old invocation ID")
				}
				if _, err := m.Stop(nestedUnitName); err != nil {
					t.Fatal(err)
				}
				c.requireDrained(t, next, "when the fresh invocation stopped")
			})
		}
	})
}
