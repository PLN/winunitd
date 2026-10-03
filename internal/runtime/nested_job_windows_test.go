//go:build windows

package runtime

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime/runtimetest/nestedjob"
	"golang.org/x/sys/windows"
)

// Workload-created nested jobs (#265), native-owner lane. Subtests are
// mode, identity and, for launch gates, phase, matching the case matrix's
// selectors. The owner (this process for SYSTEM, an S4U agent for headless)
// holds the real unit job; this process observes with held process handles.

var nestedIdentities = []string{nestedjob.IdentitySystem, nestedjob.IdentityHeadless}

func eachNestedLane(t *testing.T, identities []string, f func(t *testing.T, mode, identity string)) {
	for _, mode := range nestedjob.LaunchModes {
		t.Run(mode, func(t *testing.T) {
			for _, identity := range identities {
				t.Run(identity, func(t *testing.T) { f(t, mode, identity) })
			}
		})
	}
}

type nestedUnit struct {
	owner    nestedOwner
	obs      *nestedjob.Observer
	cfg      nestedjob.MainConfig
	rec      *nestedjob.Record
	identity string
	sid      string
	main     nestedjob.Identity
	drained  bool
}

// startNested records one scenario, selects its owner and launches MAIN.
// Cleanup stops the unit, terminates any held process that escaped it,
// releases held handles and closes the owner; the record is written last.
func startNested(t *testing.T, caseID, mode, identity, phase string, configure func(*nestedjob.MainConfig)) *nestedUnit {
	t.Helper()
	rec := nestedRecordFor(t, caseID, mode, identity, phase)
	owner, dir, sid := newNestedOwner(t, identity)
	u := &nestedUnit{owner: owner, obs: nestedjob.NewObserver(dir), rec: rec, identity: identity, sid: sid}
	rec.Token = owner.token()
	u.cfg = nestedjob.MainConfig{LaunchMode: mode, CaseDir: dir, Generation: 1, Work: nestedjob.WorkIdle, OnStop: nestedjob.OnStopCooperative}
	if configure != nil {
		configure(&u.cfg)
	}
	t.Cleanup(func() {
		var errs []error
		if err := owner.stop(5 * time.Second); err != nil {
			errs = append(errs, fmt.Errorf("unit stop: %w", err))
		}
		if err := u.obs.TerminateRunning(); err != nil {
			errs = append(errs, fmt.Errorf("terminate escaped processes: %w", err))
		}
		if err := u.obs.Close(); err != nil {
			errs = append(errs, fmt.Errorf("release held handles: %w", err))
		}
		if err := owner.close(); err != nil {
			errs = append(errs, fmt.Errorf("close owner: %w", err))
		}
		for _, err := range errs {
			t.Error(err)
		}
		rec.Cleanup(u.drained && len(errs) == 0)
		if r, err := u.obs.Report(1); t.Failed() && err == nil {
			for _, e := range r.Events {
				t.Logf("report %d %s %s %s", e.Seq, e.Kind, e.Note, e.Failure.Error())
			}
		}
	})
	u.main = owner.launch(t, u.cfg)
	return u
}

func (u *nestedUnit) inUnitJob(t *testing.T, h *nestedjob.Held) bool {
	t.Helper()
	return u.owner.inUnitJob(t, h)
}

// checkTokens requires every observed fixture process to run with the
// owner's token, and the owner's token to be the lane's identity.
func (u *nestedUnit) checkTokens(t *testing.T, ids ...nestedjob.Identity) {
	t.Helper()
	tok := u.owner.token()
	for _, id := range ids {
		if id.SID != tok.SID || id.Session != tok.Session || id.Elevated != tok.Elevated {
			t.Fatalf("%s token %s/%d/%t differs from the owner's %s/%d/%t", id.Role, id.SID, id.Session, id.Elevated, tok.SID, tok.Session, tok.Elevated)
		}
	}
	if got := nestedjob.IdentityOf(tok); got != u.identity {
		if u.identity == nestedjob.IdentityHeadless {
			t.Fatalf("owner token supports %q, not headless", got)
		}
		t.Log("owner is not SYSTEM: a generic regression run, not SYSTEM evidence")
		u.rec.Note("owner token is not SYSTEM")
	}
}

// ready waits for READY, checks the tree, both job layers and the token
// context, and returns the report and held MAIN, ENGINE, G1, G2.
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
	if !held[0].ID.Same(u.main) {
		t.Fatal("tree MAIN is not the launched unit process")
	}
	tree := r.Find(nestedjob.EventTree)
	ids := make([]nestedjob.Identity, len(tree.Tree))
	for i, m := range tree.Tree {
		ids[i] = m.Identity
	}
	u.checkTokens(t, ids...)
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
// when the stop returns: an empty job list alone is not exit evidence.
func (u *nestedUnit) stopDrained(t *testing.T, held []*nestedjob.Held) {
	t.Helper()
	if err := u.owner.stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	running, err := nestedjob.Unsignaled(held)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range running {
		late := nestedjob.WaitSignaled([]*nestedjob.Held{h}, 5*time.Second)
		t.Errorf("%s pid %d still running when the unit stop returned (after 5s more: %v)", h.ID.Role, h.ID.PID, late)
	}
	u.drained = len(running) == 0
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
		win32 := uint32(0)
		if res.Failure != nil {
			win32 = res.Failure.Win32
		}
		u.rec.Note(fmt.Sprintf("%s breakaway=%t not created win32=%d", role, breakaway, win32))
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
	eachNestedLane(t, nestedIdentities, func(t *testing.T, mode, identity string) {
		u := startNested(t, "N01", mode, identity, "", nil)
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
		pids := u.owner.pids(t)
		for _, h := range held {
			if !slices.Contains(pids, int(h.ID.PID)) {
				t.Errorf("unit job process list %v lacks %s %d", pids, h.ID.Role, h.ID.PID)
			}
		}
		tree := r.Find(nestedjob.EventTree)
		t.Logf("mode=%s identity=%s os=%s session=%d elevated=%t main=%d engine=%d g1=%d g2=%d",
			mode, identity, r.Find(nestedjob.EventStart).OS, tree.Tree[0].Session, tree.Tree[0].Elevated,
			held[0].ID.PID, held[1].ID.PID, held[2].ID.PID, held[3].ID.PID)
		u.stopDrained(t, held)
	})
}

// N02: MAIN's own last-handle close kills only its inner job.
func TestNativeNestedJobSoleClose(t *testing.T) {
	eachNestedLane(t, nestedIdentities, func(t *testing.T, mode, identity string) {
		u := startNested(t, "N02", mode, identity, "", nil)
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
			pids := u.owner.pids(t)
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
	eachNestedLane(t, nestedIdentities, func(t *testing.T, mode, identity string) {
		u := startNested(t, "N11", mode, identity, "", nil)
		_, held := u.ready(t)
		for _, role := range []string{nestedjob.RoleMain, nestedjob.RoleEngine, nestedjob.RoleG1} {
			res, h := u.probe(t, role, true)
			if h != nil {
				held = append(held, h)
				t.Logf("%s breakaway probe created in any job=%t", role, res.InAnyJob)
				u.rec.Note(fmt.Sprintf("%s breakaway created inAnyJob=%t", role, res.InAnyJob))
			}
		}
		u.stopDrained(t, held)
	})
}

// N12: an inner job that permits explicit breakaway cannot pass it through
// the unit job, which does not.
func TestNativeNestedJobInnerBreakaway(t *testing.T) {
	testNestedInnerBreakaway(t, "N12", nestedjob.InnerBreakawayExplicit, true)
}

// N13: silent breakaway from the inner job still leaves probes in the unit job.
func TestNativeNestedJobInnerSilentBreakaway(t *testing.T) {
	testNestedInnerBreakaway(t, "N13", nestedjob.InnerBreakawaySilent, false)
}

func testNestedInnerBreakaway(t *testing.T, caseID, setting string, breakaway bool) {
	eachNestedLane(t, nestedIdentities, func(t *testing.T, mode, identity string) {
		u := startNested(t, caseID, mode, identity, "", nil)
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
			inner := u.checkInner(t, h)
			t.Logf("%s probe breakaway=%t: unit job=true inner job=%t any job=%t", role, breakaway, inner, res.InAnyJob)
			u.rec.Note(fmt.Sprintf("%s probe unit=true inner=%t", role, inner))
		}
		u.stopDrained(t, held)
	})
}

// N14: MAIN dies at a held launch gate with ENGINE still suspended.
func TestNativeNestedJobLaunchGate(t *testing.T) {
	gates := map[string][]string{
		nestedjob.ModeAssign:  {nestedjob.GatePreAssign, nestedjob.GatePreResume},
		nestedjob.ModeJobList: {nestedjob.GatePreResume},
	}
	eachNestedLane(t, nestedIdentities, func(t *testing.T, mode, identity string) {
		for _, gate := range gates[mode] {
			t.Run(gate, func(t *testing.T) {
				u := startNested(t, "N14", mode, identity, gate, func(c *nestedjob.MainConfig) { c.Gate = gate })
				r, err := u.obs.WaitReport(1, "gate", func(r *nestedjob.Report) bool { return r.Find(nestedjob.EventGate) != nil })
				if err != nil {
					t.Fatal(err)
				}
				if r.Find(nestedjob.EventResumed) != nil {
					t.Fatal("ENGINE was resumed before the gate")
				}
				start := *r.Find(nestedjob.EventStart).Identity
				if !start.Same(u.main) {
					t.Fatal("gate report is not from the launched MAIN")
				}
				u.checkTokens(t, start)
				member := r.Find(nestedjob.EventGate).Tree[0]
				main, err := u.obs.Hold(start, 0)
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
				if member.InInner != (gate != nestedjob.GatePreAssign) {
					t.Fatalf("inner membership at %s = %t", gate, member.InInner)
				}
				if err := nestedjob.Terminate(main); err != nil {
					t.Fatal(err)
				}
				if err := nestedjob.WaitSignaled([]*nestedjob.Held{main}, 5*time.Second); err != nil {
					t.Fatal(err)
				}
				if gate == nestedjob.GatePreAssign {
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

// N15: sensitivity control, SYSTEM only. A deliberately inherited duplicate
// keeps the inner job alive after MAIN closes both of its copies.
func TestNativeNestedJobInheritedHandleControl(t *testing.T) {
	eachNestedLane(t, []string{nestedjob.IdentitySystem}, func(t *testing.T, mode, identity string) {
		u := startNested(t, "N15", mode, identity, "", func(c *nestedjob.MainConfig) { c.Sensitivity = nestedjob.SensitivityInheritInner })
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
