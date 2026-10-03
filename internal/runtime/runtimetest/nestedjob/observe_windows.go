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
// stage; a failed stage keeps every handle it held until it exits.
func runObserve(c ObserveConfig) error {
	obs := NewObserver(c.CaseDir)
	defer obs.Close()
	rep := ObserverReport{Schema: ObserverReportSchema, Generation: c.Generation, Replacement: c.Replacement}
	write := func() error { return WriteJSON(c.Report, rep) }
	fail := func(err error) error {
		rep.Stage, rep.Failure = StageFailed, err.Error()
		return errors.Join(err, write())
	}
	_, old, err := obs.ReadyWithin(c.Generation, c.Timeout)
	if err != nil {
		return fail(fmt.Errorf("generation %d: %w", c.Generation, err))
	}
	for _, pid := range c.HoldPIDs {
		h, err := holdPID(obs, pid, "manager", 0)
		if err != nil {
			return fail(err)
		}
		old = append(old, h)
	}
	crash, err := holdPID(obs, c.CrashPID, "crash-target", windows.PROCESS_TERMINATE)
	if err != nil {
		return fail(err)
	}
	if !strings.EqualFold(filepath.Base(crash.ID.Image), c.CrashImage) {
		return fail(fmt.Errorf("crash target pid %d is not %s", c.CrashPID, c.CrashImage))
	}
	old = append(old, crash)
	rep.Old = heldExits(old)
	rep.Stage = StageObserved
	if err := write(); err != nil {
		return err
	}
	rep.CrashAt = nowFiletime()
	if err := Terminate(crash); err != nil {
		return fail(fmt.Errorf("terminate crash target: %w", err))
	}
	ex := heldExits([]*Held{crash})[0]
	rep.Crash = &ex
	_, replacement, err := obs.ReadyWithin(c.Replacement, c.Timeout)
	if err != nil {
		rep.Old = heldExits(old)
		return fail(fmt.Errorf("replacement generation %d: %w", c.Replacement, err))
	}
	r, err := obs.Report(c.Replacement)
	if err != nil {
		return fail(err)
	}
	start := r.Find(EventStart).Identity
	rep.Old = heldExits(old)
	ordering := ClassifyOrdering(rep.Old, start.Created)
	rep.Ordering = &ordering
	if prev := r.Find(EventPrevious); prev != nil {
		rep.EntryObservation = prev.Previous
	}
	for _, h := range replacement {
		rep.New = append(rep.New, h.ID)
	}
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
	if err := WaitSignaled(append(append([]*Held(nil), old...), replacement...), 15*time.Second); err != nil {
		return fail(fmt.Errorf("cleanup: %w", err))
	}
	rep.CleanupConfirmed = true
	rep.Stage = StageFinished
	if err := write(); err != nil {
		return err
	}
	if ordering.Verdict != OrderingOrdered {
		return fmt.Errorf("ordering %s: %s", ordering.Verdict, strings.Join(ordering.NotBefore, ", "))
	}
	return nil
}

// holdPID holds a process named only by PID, recording the creation time
// and image at the moment it is held.
func holdPID(obs *Observer, pid uint32, role string, access uint32) (*Held, error) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, fmt.Errorf("open %s pid %d: %w", role, pid, err)
	}
	created, cerr := CreationTime(h)
	image, ierr := imagePath(h)
	_ = windows.CloseHandle(h)
	if err := errors.Join(cerr, ierr); err != nil {
		return nil, fmt.Errorf("identify %s pid %d: %w", role, pid, err)
	}
	return obs.Hold(Identity{Role: role, PID: pid, Created: created, Image: image}, access)
}

func heldExits(held []*Held) []HeldExit {
	out := make([]HeldExit, 0, len(held))
	for _, h := range held {
		e := HeldExit{Role: h.ID.Role, PID: h.ID.PID, Created: h.ID.Created, Image: filepath.Base(h.ID.Image)}
		if t, err := ExitTime(h.Handle); err == nil && t != 0 {
			e.Exited, e.ExitTime = true, t
		}
		out = append(out, e)
	}
	return out
}
