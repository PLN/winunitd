//go:build windows

package nestedjob

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// runObserve is the installed-daemon observer. It writes its report at each
// stage; a failed stage keeps every handle it held until it exits. The
// finished report carries raw identities and kernel times only; the
// recorder recomputes every verdict from them.
func runObserve(c ObserveConfig) error {
	obs := NewObserver(c.CaseDir)
	defer obs.Close()
	rep := ObserverReport{Schema: ObserverReportSchema, Binding: c.Binding, Generation: c.Generation, Replacement: c.Replacement}
	write := func() error { return WriteJSON(c.Report, rep) }
	fail := func(err error) error {
		rep.Stage, rep.Failure = StageFailed, err.Error()
		return errors.Join(err, write())
	}
	run, err := LoadAdmission(c.Admission)
	if err != nil {
		return fail(err)
	}
	rep.Binding = c.Binding.bind(run)
	if exe, err := ExecutableSHA256(); err != nil || !run.Manifest.Admits(exe) {
		return fail(fmt.Errorf("the observer executable is not admitted: %v", err))
	}
	_, old, err := obs.ReadyWithin(c.Generation, c.Timeout)
	if err != nil {
		return fail(fmt.Errorf("generation %d: %w", c.Generation, err))
	}
	// The unit must actually run the bound launch mode before anything is
	// crashed.
	if r, err := obs.Report(c.Generation); err != nil {
		return fail(err)
	} else if err := CheckTree(r, c.Binding.Mode); err != nil {
		return fail(fmt.Errorf("generation %d: %w", c.Generation, err))
	}
	for _, h := range old {
		rep.OldTree = append(rep.OldTree, h.ID)
	}
	var managers []*Held
	for _, pid := range c.HoldPIDs {
		h, err := holdPID(obs, pid, CrashUserManager, 0)
		if err != nil {
			return fail(err)
		}
		managers = append(managers, h)
	}
	crash, err := holdPID(obs, c.CrashPID, c.Binding.CrashRole, windows.PROCESS_TERMINATE)
	if err != nil {
		return fail(err)
	}
	if !strings.EqualFold(filepath.Base(crash.ID.Image), DaemonImage) {
		return fail(fmt.Errorf("crash target pid %d is not %s", c.CrashPID, DaemonImage))
	}
	all := append(append(append([]*Held(nil), old...), managers...), crash)
	rep.Old = heldExits(all)
	rep.Stage = StageObserved
	if err := write(); err != nil {
		return err
	}
	rep.CrashAt = nowFiletime()
	if err := Terminate(crash); err != nil {
		return fail(fmt.Errorf("terminate crash target: %w", err))
	}
	if err := WaitSignaled([]*Held{crash}, ObserveTimeout); err != nil {
		return fail(fmt.Errorf("crash target: %w", err))
	}
	_, replacement, err := obs.ReadyWithin(c.Replacement, c.Timeout)
	if err != nil {
		rep.Old = heldExits(all)
		return fail(fmt.Errorf("replacement generation %d: %w", c.Replacement, err))
	}
	r, err := obs.Report(c.Replacement)
	if err != nil {
		return fail(err)
	}
	for _, h := range replacement {
		rep.NewTree = append(rep.NewTree, h.ID)
	}
	if start := r.Find(EventStart); start == nil || len(rep.NewTree) == 0 || !start.Identity.Same(rep.NewTree[0]) {
		return fail(errors.New("replacement report does not start with its tree's MAIN"))
	}
	if err := CheckTree(r, c.Binding.Mode); err != nil {
		return fail(fmt.Errorf("replacement generation %d: %w", c.Replacement, err))
	}
	rep.ReplacementCreated = rep.NewTree[0].Created
	rep.Old = heldExits(all)
	ex := heldExits([]*Held{crash})[0]
	rep.Crash = &ex
	rep.Managers = heldExits(managers)
	ordering := ClassifyOrdering(rep.Old, rep.ReplacementCreated)
	rep.Ordering = &ordering
	rep.Stage = StageReplaced
	if err := write(); err != nil {
		return err
	}
	// The driver stops and removes the unit, then creates the finish file.
	deadline := time.Now().Add(c.Timeout)
	for {
		if _, err := os.Stat(c.FinishFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fail(errors.New("driver did not finish the case"))
		}
		time.Sleep(pollInterval)
	}
	if err := WaitSignaled(append(append([]*Held(nil), all...), replacement...), 15*time.Second); err != nil {
		return fail(fmt.Errorf("cleanup: %w", err))
	}
	// Both MAINs have exited: their reports are final and must be complete.
	for _, g := range []struct {
		gen    int
		events *[]Event
	}{{c.Generation, &rep.OldEvents}, {c.Replacement, &rep.NewEvents}} {
		final, err := ReadFinalReport(c.CaseDir, g.gen)
		if err != nil {
			return fail(fmt.Errorf("final report of generation %d: %w", g.gen, err))
		}
		*g.events = final.Events
	}
	rep.CleanupConfirmed = true
	rep.Stage = StageFinished
	if err := write(); err != nil {
		return err
	}
	return ValidateDaemonReport(rep, rep.Binding, run.Manifest)
}

// holdPID holds a process named only by PID, recording the creation time,
// image and account at the moment it is held.
func holdPID(obs *Observer, pid uint32, role string, access uint32) (*Held, error) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, fmt.Errorf("open %s pid %d: %w", role, pid, err)
	}
	created, cerr := CreationTime(h)
	image, ierr := imagePath(h)
	sid, serr := processSID(h)
	_ = windows.CloseHandle(h)
	if err := errors.Join(cerr, ierr, serr); err != nil {
		return nil, fmt.Errorf("identify %s pid %d: %w", role, pid, err)
	}
	return obs.Hold(Identity{Role: role, PID: pid, Created: created, Image: image, SID: sid}, access)
}

// heldExits reports each held process and, once its handle is signaled,
// its kernel exit time.
func heldExits(held []*Held) []HeldExit {
	out := make([]HeldExit, 0, len(held))
	for _, h := range held {
		e := HeldExit{Role: h.ID.Role, PID: h.ID.PID, Created: h.ID.Created, SID: h.ID.SID}
		if h.ID.Image != "" {
			e.Image = filepath.Base(h.ID.Image)
		}
		if t, err := ExitTime(h.Handle); err == nil && t != 0 {
			e.Exited, e.ExitTime = true, t
		}
		out = append(out, e)
	}
	return out
}
