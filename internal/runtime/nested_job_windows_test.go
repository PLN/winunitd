//go:build windows

package runtime

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime/runtimetest/nestedjob"
	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows"
)

// Workload-created nested jobs (#265), owner lane: this test process owns the
// real unit job from the Windows launcher and observes the fixture's tree with
// held process handles. Rows refer to the nestedjob case matrix.

type nestedUnit struct {
	proc *winProc
	obs  *nestedjob.Observer
	cfg  nestedjob.MainConfig
}

func nestedConfig(t *testing.T, mode string) nestedjob.MainConfig {
	t.Helper()
	return nestedjob.MainConfig{
		LaunchMode: mode, CaseDir: t.TempDir(), Generation: 1,
		Work: nestedjob.WorkIdle, OnStop: nestedjob.OnStopCooperative,
	}
}

// startNested launches MAIN as a Type=simple unit through the real launcher.
// Cleanup stops the unit job, then terminates any held process that escaped
// it, then releases the observer's handles.
func startNested(t *testing.T, cfg nestedjob.MainConfig) *nestedUnit {
	t.Helper()
	argv := append([]string{testAbs(t), nestedjob.HelperSelector}, cfg.Args()...)
	p, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Unit: "nested.service", Type: unit.TypeSimple, Argv: argv, Dir: cfg.CaseDir, Env: helperEnv(),
	})
	if err != nil {
		t.Fatal(err)
	}
	u := &nestedUnit{proc: p.(*winProc), obs: nestedjob.NewObserver(cfg.CaseDir), cfg: cfg}
	var mu sync.Mutex
	var stderr bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := p.Stderr().Read(buf)
			mu.Lock()
			if stderr.Len() < 64<<10 {
				stderr.Write(buf[:n])
			}
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		if err := p.Stop(5 * time.Second); err != nil {
			t.Errorf("unit stop: %v", err)
		}
		if err := u.obs.TerminateRunning(); err != nil {
			t.Errorf("terminate escaped processes: %v", err)
		}
		if err := u.obs.Close(); err != nil {
			t.Errorf("release held handles: %v", err)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		mu.Lock()
		defer mu.Unlock()
		if t.Failed() && stderr.Len() > 0 {
			t.Logf("fixture stderr:\n%s", stderr.String())
		}
		if r, err := u.obs.Report(1); t.Failed() && err == nil {
			for _, e := range r.Events {
				t.Logf("report %d %s %s %s", e.Seq, e.Kind, e.Note, e.Failure.Error())
			}
		}
	})
	return u
}

// inUnitJob checks membership of an exact held process in the unit job.
func (u *nestedUnit) inUnitJob(t *testing.T, h *nestedjob.Held) bool {
	t.Helper()
	u.proc.job.mu.Lock()
	defer u.proc.job.mu.Unlock()
	if u.proc.job.handle == 0 {
		t.Fatal("unit job is closed")
	}
	in, err := isProcessInJob(h.Handle, u.proc.job.handle)
	if err != nil {
		t.Fatalf("unit-job membership of %s: %v", h.ID.Role, err)
	}
	return in
}

// ready waits for READY, checks the tree and both job layers, and returns the
// report and held MAIN, ENGINE, G1, G2.
func (u *nestedUnit) ready(t *testing.T) (*nestedjob.Report, []*nestedjob.Held) {
	t.Helper()
	_, held, err := u.obs.Ready(1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := u.obs.Report(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := nestedjob.CheckTree(r, u.cfg.LaunchMode); err != nil {
		t.Fatal(err)
	}
	created, err := nestedjob.CreationTime(u.proc.process)
	if err != nil {
		t.Fatal(err)
	}
	if held[0].ID.PID != uint32(u.proc.PID()) || held[0].ID.Created != created {
		t.Fatal("tree MAIN is not the launched unit process")
	}
	for _, h := range held {
		if done, err := h.Signaled(); err != nil || done {
			t.Fatalf("%s is not running at READY: %v", h.ID.Role, err)
		}
		if !u.inUnitJob(t, h) {
			t.Fatalf("%s is not in the unit job", h.ID.Role)
		}
	}
	return r, held
}

func (u *nestedUnit) checkInner(t *testing.T, h *nestedjob.Held) bool {
	t.Helper()
	ack, err := u.obs.Command(1, nestedjob.RoleMain, nestedjob.Command{Verb: nestedjob.VerbCheckInner, PID: h.ID.PID, Created: h.ID.Created})
	if err != nil {
		t.Fatal(err)
	}
	if ack.InInner == nil {
		t.Fatal("check-inner acknowledged without a result")
	}
	return *ack.InInner
}

// stopDrained stops the unit and requires every held process to be signaled
// when Stop returns: an empty job list alone is not exit evidence.
func (u *nestedUnit) stopDrained(t *testing.T, held []*nestedjob.Held) {
	t.Helper()
	if err := u.proc.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	running, err := nestedjob.Unsignaled(held)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range running {
		late := nestedjob.WaitSignaled([]*nestedjob.Held{h}, 5*time.Second)
		t.Errorf("%s pid %d still running when unit Stop returned (after 5s more: %v)", h.ID.Role, h.ID.PID, late)
	}
}

// probe asks role to create one suspended probe, holds it when created and
// requires that it is still in the unit job.
func (u *nestedUnit) probe(t *testing.T, role string, breakaway bool) (nestedjob.ProbeResult, *nestedjob.Held) {
	t.Helper()
	ack, err := u.obs.Command(1, role, nestedjob.Command{Verb: nestedjob.VerbProbe, Breakaway: breakaway})
	if err != nil {
		t.Fatal(err)
	}
	if ack.Probe == nil {
		t.Fatalf("%s probe acknowledged without a result", role)
	}
	res := *ack.Probe
	if !res.Created {
		t.Logf("%s probe breakaway=%t: not created: %s", role, breakaway, res.Failure.Error())
		return res, nil
	}
	if res.Identity == nil {
		t.Fatalf("%s probe created without identity: %s", role, res.Failure.Error())
	}
	h, err := u.obs.Hold(*res.Identity, windows.PROCESS_TERMINATE)
	if err != nil {
		t.Fatal(err)
	}
	if !u.inUnitJob(t, h) {
		t.Errorf("%s probe breakaway=%t escaped the unit job", role, breakaway)
	}
	threads, count, err := nestedjob.PrimarySuspendCount(h.ID.PID)
	if err != nil || threads != 1 || count != 1 {
		t.Errorf("%s probe ran: threads=%d suspend=%d err=%v", role, threads, count, err)
	}
	return res, h
}

// N01: both launch modes establish the tree with simultaneous membership.
func TestNativeNestedJobContainment(t *testing.T) {
	for _, mode := range nestedjob.LaunchModes {
		t.Run(mode, func(t *testing.T) {
			u := startNested(t, nestedConfig(t, mode))
			r, held := u.ready(t)
			statuses := map[string]nestedjob.RoleStatus{}
			for _, role := range []string{nestedjob.RoleEngine, nestedjob.RoleG1, nestedjob.RoleG2} {
				st, err := u.obs.Status(1, role)
				if err != nil {
					t.Fatal(err)
				}
				statuses[role] = st
			}
			if err := nestedjob.CheckHandleProbes(statuses, r.Find(nestedjob.EventTree).Inner.Handle, ""); err != nil {
				t.Fatal(err)
			}
			pids, err := u.proc.job.PIDs()
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range held {
				if !slices.Contains(pids, int(h.ID.PID)) {
					t.Errorf("unit job process list %v lacks %s %d", pids, h.ID.Role, h.ID.PID)
				}
			}
			tree := r.Find(nestedjob.EventTree)
			t.Logf("mode=%s os=%s session=%d elevated=%t main=%d engine=%d g1=%d g2=%d",
				mode, r.Find(nestedjob.EventStart).OS, tree.Tree[0].Session, tree.Tree[0].Elevated,
				held[0].ID.PID, held[1].ID.PID, held[2].ID.PID, held[3].ID.PID)
			u.stopDrained(t, held)
		})
	}
}

// N02: MAIN's own last-handle close kills only its inner job.
func TestNativeNestedJobSoleClose(t *testing.T) {
	for _, mode := range nestedjob.LaunchModes {
		t.Run(mode, func(t *testing.T) {
			u := startNested(t, nestedConfig(t, mode))
			_, held := u.ready(t)
			ack, err := u.obs.Command(1, nestedjob.RoleMain, nestedjob.Command{Verb: nestedjob.VerbCloseInner})
			if err != nil || ack.Closed != 1 {
				t.Fatalf("close inner: %+v %v", ack, err)
			}
			if err := nestedjob.WaitSignaled(held[1:], 5*time.Second); err != nil {
				t.Fatalf("inner last-handle close: %v", err)
			}
			if done, err := held[0].Signaled(); err != nil || done {
				t.Fatalf("MAIN exited with its inner job: %v", err)
			}
			if _, err := u.obs.Command(1, nestedjob.RoleMain, nestedjob.Command{Verb: nestedjob.VerbPing}); err != nil {
				t.Fatalf("MAIN unresponsive after closing its job: %v", err)
			}
			if !u.inUnitJob(t, held[0]) {
				t.Fatal("MAIN left the unit job")
			}
			// Only MAIN (and a console host the launcher may give it) remains.
			deadline := time.Now().Add(5 * time.Second)
			for {
				pids, err := u.proc.job.PIDs()
				if err != nil {
					t.Fatal(err)
				}
				var others []string
				for _, pid := range pids {
					if pid != int(held[0].ID.PID) {
						if image := processImageBase(pid); !strings.EqualFold(image, "conhost.exe") {
							others = append(others, fmt.Sprintf("%d %s", pid, image))
						}
					}
				}
				if slices.Contains(pids, int(held[0].ID.PID)) && len(others) == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("unit job lists %v after the inner tree exited: %v", pids, others)
				}
				time.Sleep(20 * time.Millisecond)
			}
			u.stopDrained(t, held)
		})
	}
}

func processImageBase(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "unknown: " + err.Error()
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "unknown: " + err.Error()
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}

// N11: no breakaway request leaves the unit job while no job permits it.
func TestNativeNestedJobBreakawayDenied(t *testing.T) {
	for _, mode := range nestedjob.LaunchModes {
		t.Run(mode, func(t *testing.T) {
			u := startNested(t, nestedConfig(t, mode))
			_, held := u.ready(t)
			for _, role := range []string{nestedjob.RoleMain, nestedjob.RoleEngine, nestedjob.RoleG1} {
				res, h := u.probe(t, role, true)
				if h != nil {
					held = append(held, h)
					t.Logf("%s breakaway probe created in any job=%t", role, res.InAnyJob)
				}
			}
			u.stopDrained(t, held)
		})
	}
}

// N12: an inner job that permits explicit breakaway cannot pass it through
// the unit job, which does not.
func TestNativeNestedJobInnerBreakaway(t *testing.T) {
	testNestedInnerBreakaway(t, nestedjob.InnerBreakawayExplicit, true)
}

// N13: silent breakaway from the inner job still leaves probes in the unit job.
func TestNativeNestedJobInnerSilentBreakaway(t *testing.T) {
	testNestedInnerBreakaway(t, nestedjob.InnerBreakawaySilent, false)
}

func testNestedInnerBreakaway(t *testing.T, setting string, breakaway bool) {
	for _, mode := range nestedjob.LaunchModes {
		t.Run(mode, func(t *testing.T) {
			u := startNested(t, nestedConfig(t, mode))
			_, held := u.ready(t)
			ack, err := u.obs.Command(1, nestedjob.RoleMain, nestedjob.Command{Verb: nestedjob.VerbSetInnerBreakaway, InnerBreakaway: setting})
			if err != nil {
				t.Fatal(err)
			}
			explicit, silent := nestedjob.BreakawayFlags(ack.LimitFlags)
			if explicit != (setting == nestedjob.InnerBreakawayExplicit) || silent != (setting == nestedjob.InnerBreakawaySilent) {
				t.Fatalf("inner flags %#x after %s", ack.LimitFlags, setting)
			}
			// The flag change does not detach existing members.
			for _, h := range held[1:] {
				if !u.checkInner(t, h) {
					t.Fatalf("%s left the inner job when its flags changed", h.ID.Role)
				}
			}
			for _, role := range []string{nestedjob.RoleEngine, nestedjob.RoleG1} {
				res, h := u.probe(t, role, breakaway)
				if h == nil {
					continue
				}
				held = append(held, h)
				t.Logf("%s probe breakaway=%t: unit job=true inner job=%t any job=%t",
					role, breakaway, u.checkInner(t, h), res.InAnyJob)
			}
			u.stopDrained(t, held)
		})
	}
}

// N14: MAIN dies at a held launch gate with ENGINE still suspended.
func TestNativeNestedJobLaunchGate(t *testing.T) {
	gates := map[string][]string{
		nestedjob.ModeAssign:  {nestedjob.GateBeforeAssign, nestedjob.GateBeforeResume},
		nestedjob.ModeJobList: {nestedjob.GateBeforeResume},
	}
	for _, mode := range nestedjob.LaunchModes {
		t.Run(mode, func(t *testing.T) {
			for _, gate := range gates[mode] {
				t.Run(gate, func(t *testing.T) {
					cfg := nestedConfig(t, mode)
					cfg.Gate = gate
					u := startNested(t, cfg)
					r, err := u.obs.WaitReport(1, "gate", func(r *nestedjob.Report) bool { return r.Find(nestedjob.EventGate) != nil })
					if err != nil {
						t.Fatal(err)
					}
					if r.Find(nestedjob.EventResumed) != nil {
						t.Fatal("ENGINE was resumed before the gate")
					}
					member := r.Find(nestedjob.EventGate).Tree[0]
					main, err := u.obs.Hold(*r.Find(nestedjob.EventStart).Identity, 0)
					if err != nil {
						t.Fatal(err)
					}
					child, err := u.obs.Hold(member.Identity, 0)
					if err != nil {
						t.Fatal(err)
					}
					threads, count, err := nestedjob.PrimarySuspendCount(child.ID.PID)
					if err != nil || threads != 1 || count != 1 {
						t.Fatalf("held child: threads=%d suspend=%d err=%v", threads, count, err)
					}
					if !u.inUnitJob(t, child) {
						t.Fatal("suspended child is outside the unit job")
					}
					if member.InInner != (gate != nestedjob.GateBeforeAssign) {
						t.Fatalf("inner membership at %s = %t", gate, member.InInner)
					}
					if err := nestedjob.Terminate(main); err != nil {
						t.Fatal(err)
					}
					if err := nestedjob.WaitSignaled([]*nestedjob.Held{main}, 5*time.Second); err != nil {
						t.Fatal(err)
					}
					if gate == nestedjob.GateBeforeAssign {
						// Only the unit job contains an unassigned child.
						time.Sleep(500 * time.Millisecond)
						if done, err := child.Signaled(); err != nil || done {
							t.Fatalf("unassigned child ended with its creator: %v", err)
						}
						if !u.inUnitJob(t, child) {
							t.Fatal("unassigned child left the unit job")
						}
						if threads, count, err := nestedjob.PrimarySuspendCount(child.ID.PID); err != nil || threads != 1 || count != 1 {
							t.Fatalf("unassigned child ran: threads=%d suspend=%d err=%v", threads, count, err)
						}
					} else if err := nestedjob.WaitSignaled([]*nestedjob.Held{child}, 5*time.Second); err != nil {
						t.Fatalf("inner job did not end with its creator: %v", err)
					}
					u.stopDrained(t, []*nestedjob.Held{main, child})
				})
			}
		})
	}
}

// N15: sensitivity control. A deliberately inherited duplicate keeps the
// inner job alive after MAIN closes both of its copies, and the probe sees it.
func TestNativeNestedJobInheritedHandleControl(t *testing.T) {
	for _, mode := range nestedjob.LaunchModes {
		t.Run(mode, func(t *testing.T) {
			cfg := nestedConfig(t, mode)
			cfg.Sensitivity = nestedjob.SensitivityInheritInner
			u := startNested(t, cfg)
			r, held := u.ready(t)
			engine, err := u.obs.Status(1, nestedjob.RoleEngine)
			if err != nil {
				t.Fatal(err)
			}
			statuses := map[string]nestedjob.RoleStatus{nestedjob.RoleEngine: engine}
			for _, role := range []string{nestedjob.RoleG1, nestedjob.RoleG2} {
				if statuses[role], err = u.obs.Status(1, role); err != nil {
					t.Fatal(err)
				}
			}
			if err := nestedjob.CheckHandleProbes(statuses, engine.HandleProbe.Value, nestedjob.RoleEngine); err != nil {
				t.Fatal(err)
			}
			if engine.HandleProbe.Value == r.Find(nestedjob.EventTree).Inner.Handle {
				t.Fatal("ENGINE probed MAIN's original handle value, not the duplicate")
			}
			if _, err := u.obs.Command(1, nestedjob.RoleMain, nestedjob.Command{Verb: nestedjob.VerbCloseInner}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Second)
			if running, err := nestedjob.Unsignaled(held[1:]); err != nil || len(running) != 3 {
				t.Fatalf("inherited handle did not keep the tree alive: %d running, %v", len(running), err)
			}
			if _, err := u.obs.Send(1, nestedjob.RoleEngine, nestedjob.Command{Verb: nestedjob.VerbCloseInherited}); err != nil {
				t.Fatal(err)
			}
			if err := nestedjob.WaitSignaled(held[1:], 5*time.Second); err != nil {
				t.Fatalf("last handle close: %v", err)
			}
			if done, err := held[0].Signaled(); err != nil || done {
				t.Fatalf("MAIN exited: %v", err)
			}
			u.stopDrained(t, held)
		})
	}
}
