//go:build windows

package nestedjob

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ObserveTimeout bounds each observer wait for fixture progress.
const ObserveTimeout = 15 * time.Second

// Held is an exact process the observer keeps open until teardown. An open
// handle keeps the PID reserved, so while it is unsignaled a PID-based query
// (for example UnitJob.Contains) refers to this process.
type Held struct {
	ID     Identity
	Handle windows.Handle
}

// Signaled reports whether the held process has exited.
func (h *Held) Signaled() (bool, error) { return signaled(h.Handle) }

// Observer drives one case directory from outside the fixture. It holds
// process handles only; it never opens or duplicates a job handle.
type Observer struct {
	CaseDir string
	held    []*Held
	seq     map[string]int
}

// NewObserver returns an observer for caseDir.
func NewObserver(caseDir string) *Observer {
	return &Observer{CaseDir: caseDir, seq: map[string]int{}}
}

func (o *Observer) genDir(gen int) string { return filepath.Join(o.CaseDir, GenerationDir(gen)) }

// Report reads the current report of gen.
func (o *Observer) Report(gen int) (*Report, error) { return ReadReport(o.CaseDir, gen) }

// WaitReport polls gen's report until done accepts it. A fatal or
// unqualified event ends the wait with its failure.
func (o *Observer) WaitReport(gen int, what string, done func(*Report) bool) (*Report, error) {
	deadline := time.Now().Add(ObserveTimeout)
	for {
		r, err := o.Report(gen)
		var pathErr *fs.PathError
		if err != nil && !errors.As(err, &pathErr) {
			return nil, err
		}
		if err == nil {
			if f := r.Fatal(); f != nil {
				return r, fmt.Errorf("fixture %s: %s", f.Kind, f.Failure.Error())
			}
			if done(r) {
				return r, nil
			}
		}
		if time.Now().After(deadline) {
			return r, fmt.Errorf("timed out waiting for %s in generation %d (last read: %v)", what, gen, err)
		}
		time.Sleep(pollInterval)
	}
}

// WaitManifest waits until generation.json names gen at stage.
func (o *Observer) WaitManifest(gen int, stage string) (*Manifest, error) {
	deadline := time.Now().Add(ObserveTimeout)
	for {
		m, err := ReadManifest(o.CaseDir)
		if err == nil && m.Generation == gen && m.Stage == stage {
			return m, nil
		}
		var pathErr *fs.PathError
		if err != nil && !errors.As(err, &pathErr) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return m, fmt.Errorf("timed out waiting for generation %d stage %s (last read: %v)", gen, stage, err)
		}
		time.Sleep(pollInterval)
	}
}

// Hold opens an exact identity with SYNCHRONIZE and query rights plus access.
func (o *Observer) Hold(id Identity, access uint32) (*Held, error) {
	h, err := openIdentity(id, windows.SYNCHRONIZE|access)
	if err != nil {
		return nil, err
	}
	held := &Held{ID: id, Handle: h}
	o.held = append(o.held, held)
	return held, nil
}

// Ready waits for gen's tree, holds MAIN, ENGINE, G1 and G2, acknowledges
// the observation and waits for READY. The returned handles are in tree order.
func (o *Observer) Ready(gen int) (*Event, []*Held, error) {
	r, err := o.WaitReport(gen, "tree", func(r *Report) bool { return r.Find(EventTree) != nil })
	if err != nil {
		return nil, nil, err
	}
	tree := *r.Find(EventTree)
	held := make([]*Held, 0, len(tree.Tree))
	for _, m := range tree.Tree {
		h, err := o.Hold(m.Identity, 0)
		if err != nil {
			return &tree, held, err
		}
		held = append(held, h)
	}
	if _, err := o.Command(gen, RoleMain, Command{Verb: VerbObserved}); err != nil {
		return &tree, held, err
	}
	if _, err := o.WaitReport(gen, "ready", (*Report).Ready); err != nil {
		return &tree, held, err
	}
	return &tree, held, nil
}

// Send writes the next command for role without waiting for its ack.
func (o *Observer) Send(gen int, role string, cmd Command) (int, error) {
	key := fmt.Sprintf("%d/%s", gen, role)
	o.seq[key]++
	cmd.Seq = o.seq[key]
	if err := ValidateCommand(role, cmd); err != nil {
		return cmd.Seq, err
	}
	return cmd.Seq, WriteJSON(filepath.Join(o.genDir(gen), CommandFile(role, cmd.Seq)), cmd)
}

// Command sends one command and waits for its acknowledgment. A negative
// acknowledgment is returned with its failure as the error.
func (o *Observer) Command(gen int, role string, cmd Command) (Ack, error) {
	seq, err := o.Send(gen, role, cmd)
	if err != nil {
		return Ack{}, err
	}
	path := filepath.Join(o.genDir(gen), AckFile(role, seq))
	deadline := time.Now().Add(ObserveTimeout)
	for {
		var ack Ack
		err := ReadJSON(path, &ack)
		if err == nil {
			if !ack.OK || ack.Seq != seq {
				return ack, fmt.Errorf("%s %s rejected: %s", role, cmd.Verb, ack.Failure.Error())
			}
			return ack, nil
		}
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) {
			return Ack{}, err
		}
		if time.Now().After(deadline) {
			return Ack{}, fmt.Errorf("timed out waiting for %s %s acknowledgment (last read: %v)", role, cmd.Verb, err)
		}
		time.Sleep(pollInterval)
	}
}

// Status reads a role's self-written status.
func (o *Observer) Status(gen int, role string) (RoleStatus, error) {
	var st RoleStatus
	err := ReadJSON(filepath.Join(o.genDir(gen), StatusFile(role)), &st)
	return st, err
}

// Work reads a role's work-result file.
func (o *Observer) Work(gen int, role string) (WorkStatus, error) {
	var st WorkStatus
	err := ReadJSON(filepath.Join(o.genDir(gen), WorkFile(role)), &st)
	return st, err
}

// StopHelpers lists every ExecStop helper record of gen.
func (o *Observer) StopHelpers(gen int) ([]StopHelperStatus, error) {
	entries, err := os.ReadDir(o.genDir(gen))
	if err != nil {
		return nil, err
	}
	var out []StopHelperStatus
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "stop-helper-") || strings.Contains(name, ".tmp-") {
			continue
		}
		var st StopHelperStatus
		if err := ReadJSON(filepath.Join(o.genDir(gen), name), &st); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// WaitSignaled waits until every held process has exited.
func WaitSignaled(held []*Held, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for _, h := range held {
		for {
			done, err := h.Signaled()
			if err != nil {
				return fmt.Errorf("%s pid %d: %w", h.ID.Role, h.ID.PID, err)
			}
			if done {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s pid %d is still running", h.ID.Role, h.ID.PID)
			}
			time.Sleep(pollInterval)
		}
	}
	return nil
}

// Unsignaled returns the held processes that are still running.
func Unsignaled(held []*Held) ([]*Held, error) {
	var out []*Held
	for _, h := range held {
		done, err := h.Signaled()
		if err != nil {
			return nil, err
		}
		if !done {
			out = append(out, h)
		}
	}
	return out, nil
}

// Close terminates nothing; it releases every held handle. Call it only after
// the case has confirmed exit or terminated escaped probes explicitly.
func (o *Observer) Close() error {
	var errs []error
	for _, h := range o.held {
		if err := windows.CloseHandle(h.Handle); err != nil {
			errs = append(errs, err)
		}
	}
	o.held = nil
	return errors.Join(errs...)
}

// TerminateRunning terminates held processes that are still running. Tests
// use it only to clean up after a failed assertion, such as an escaped probe.
func (o *Observer) TerminateRunning() error {
	var errs []error
	for _, h := range o.held {
		done, err := h.Signaled()
		if err != nil || done {
			continue
		}
		t, err := openIdentity(h.ID, windows.PROCESS_TERMINATE)
		if err == nil {
			err = windows.TerminateProcess(t, 1)
			_ = windows.CloseHandle(t)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("terminate %s pid %d: %w", h.ID.Role, h.ID.PID, err))
		}
	}
	return errors.Join(errs...)
}

// Terminate kills one exact held process, as a crash injection.
func Terminate(h *Held) error {
	t, err := openIdentity(h.ID, windows.PROCESS_TERMINATE)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(t)
	return windows.TerminateProcess(t, 1)
}

// PrimarySuspendCount returns the thread count of pid and the suspend count of
// its only thread. Briefly suspending and resuming leaves it unchanged.
func PrimarySuspendCount(pid uint32) (threads int, count uint32, err error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, 0, err
	}
	defer windows.CloseHandle(snap)
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		threads++
		th, oerr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if oerr != nil {
			return threads, 0, oerr
		}
		previous, serr := suspendThread(th)
		if serr == nil {
			_, serr = windows.ResumeThread(th)
		}
		_ = windows.CloseHandle(th)
		if serr != nil {
			return threads, 0, serr
		}
		count = previous
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return threads, count, err
	}
	return threads, count, nil
}
