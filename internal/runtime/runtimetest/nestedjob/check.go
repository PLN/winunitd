package nestedjob

import (
	"errors"
	"fmt"
)

// Native job constants checked by CheckTree (winnt.h).
const (
	jobLimitKillOnJobClose = 0x00002000
	jobLimitBreakawayOK    = 0x00000800
	jobLimitSilentOK       = 0x00001000
)

// CheckTree validates a READY report: the tree, its process ancestry and
// token identity, the inner job's flags and the launch sequence of mode. It
// does not prove membership in the unit job; only the job's owner can.
func CheckTree(r *Report, mode string) error {
	if r == nil || !r.Ready() {
		return errors.New("report is not ready")
	}
	if r.Truncated {
		return errors.New("report was truncated")
	}
	if f := r.Fatal(); f != nil {
		return fmt.Errorf("fixture %s: %s", f.Kind, SafeFailure(f.Failure))
	}
	start, tree := r.Find(EventStart), r.Find(EventTree)
	if start.Mode != mode {
		return fmt.Errorf("launch mode %q, want %q", start.Mode, mode)
	}
	main, engine, g1, g2 := tree.Tree[0], tree.Tree[1], tree.Tree[2], tree.Tree[3]
	if !main.Same(*start.Identity) {
		return errors.New("tree MAIN differs from the started MAIN")
	}
	if main.InInner || !engine.InInner || !g1.InInner || !g2.InInner {
		return fmt.Errorf("inner membership main=%t engine=%t g1=%t g2=%t, want only descendants",
			main.InInner, engine.InInner, g1.InInner, g2.InInner)
	}
	if engine.ParentPID != main.PID || engine.ParentCreated != main.Created {
		return errors.New("ENGINE was not created by MAIN")
	}
	for _, leaf := range []Member{g1, g2} {
		if leaf.ParentPID != engine.PID || leaf.ParentCreated != engine.Created {
			return fmt.Errorf("%s was not created by ENGINE", leaf.Role)
		}
	}
	for _, m := range tree.Tree[1:] {
		if m.SID != main.SID || m.Session != main.Session || m.Elevated != main.Elevated ||
			m.ImageSHA256 != main.ImageSHA256 || m.Generation != main.Generation || m.Invocation != main.Invocation {
			return fmt.Errorf("%s identity differs from MAIN", m.Role)
		}
	}
	if err := checkInner(*tree.Inner); err != nil {
		return err
	}
	var order []string
	for _, e := range r.Events {
		switch e.Kind {
		case EventCreated, EventAssigned, EventResumed, EventTree, EventReady:
			if e.Identity != nil && !e.Identity.Same(engine.Identity) {
				return fmt.Errorf("%s event names another process", e.Kind)
			}
			order = append(order, e.Kind)
		case EventGate:
			return errors.New("a READY tree passed a launch gate")
		}
	}
	want := []string{EventCreated, EventResumed, EventTree, EventReady}
	if mode == ModeAssign {
		want = []string{EventCreated, EventAssigned, EventResumed, EventTree, EventReady}
	}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		return fmt.Errorf("launch sequence %v, want %v", order, want)
	}
	return nil
}

func checkInner(inner InnerJob) error {
	if inner.LimitFlags != jobLimitKillOnJobClose {
		return fmt.Errorf("inner limit flags %#x, want kill-on-close only", inner.LimitFlags)
	}
	if inner.CPUControlFlags != 0 || inner.CPUQueryError != "" {
		return fmt.Errorf("inner CPU control %#x %q, want none", inner.CPUControlFlags, inner.CPUQueryError)
	}
	if inner.Inheritable {
		return errors.New("inner job handle is inheritable")
	}
	return nil
}

// ErrUnqualified marks a documented missing prerequisite, such as the
// JOB_LIST attribute on an OS older than Windows 10. It is a skip with the
// reason, never a pass and never a silent fallback to another mode.
var ErrUnqualified = errors.New("unqualified")

// ErrorInvalidHandle is the Win32 code of a job query at a value that is no
// handle, or a handle to another object type.
const ErrorInvalidHandle = 6

// CheckHandleProbes validates the negative inheritance probe of ENGINE, G1
// and G2: none may find a job at the probed value, unless inherited names the
// one role that deliberately received a duplicate (the sensitivity control).
// Only ERROR_INVALID_HANDLE shows that no job handle is there; any other
// query error, such as an inherited handle without query access, is not
// evidence either way and fails the check.
func CheckHandleProbes(statuses map[string]RoleStatus, value uint64, inherited string) error {
	for _, role := range []string{RoleEngine, RoleG1, RoleG2} {
		st, ok := statuses[role]
		if !ok {
			return fmt.Errorf("%s status missing", role)
		}
		p := st.HandleProbe
		if p.Value != value {
			return fmt.Errorf("%s probed %#x, want %#x", role, p.Value, value)
		}
		if p.IsJob != (role == inherited) {
			return fmt.Errorf("%s found a job at %#x: %t", role, value, p.IsJob)
		}
		if !p.IsJob && p.Win32 != ErrorInvalidHandle {
			return fmt.Errorf("%s probe at %#x is inconclusive: win32 %d", role, value, p.Win32)
		}
	}
	return nil
}

// BreakawayFlags reports which breakaway limits a job's flags permit.
func BreakawayFlags(flags uint32) (explicit, silent bool) {
	return flags&jobLimitBreakawayOK != 0, flags&jobLimitSilentOK != 0
}
