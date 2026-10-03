//go:build windows

package nestedjob

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Functional bounds. A case driver adds its own observation slack on top.
const (
	statusTimeout   = 15 * time.Second
	observedTimeout = 60 * time.Second
	stopWaitTimeout = 10 * time.Second
	helperMainWait  = 30 * time.Second
	pollInterval    = 10 * time.Millisecond
)

func run(inv Invocation, prefix []string) error {
	switch {
	case inv.Main != nil:
		return runMain(*inv.Main, prefix)
	case inv.Engine != nil:
		return runEngine(*inv.Engine, prefix)
	case inv.Leaf != nil:
		return runLeaf(*inv.Leaf, prefix)
	case inv.Stop != nil:
		return runStop(*inv.Stop)
	case inv.Observe != nil:
		return runObserve(*inv.Observe)
	}
	return errors.New("no role selected")
}

func selfArgv(prefix, roleArgs []string) ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	argv := append([]string{exe}, prefix...)
	return append(argv, roleArgs...), nil
}

// heldProcess retains a created process and, until resume, its thread.
type heldProcess struct {
	id      Identity
	process windows.Handle
	thread  windows.Handle
}

type mainRole struct {
	cfg    MainConfig
	prefix []string
	dir    string
	self   Identity
	report *reportWriter
	inner  windows.Handle
	engine *heldProcess
	leaves []*heldProcess
	// Created probes stay suspended and owned until this process exits.
	probes  []*heldProcess
	tree    []Identity
	ready   bool
	ignored bool
}

func runMain(c MainConfig, prefix []string) error {
	prev, err := ReadManifest(c.CaseDir)
	if errors.Is(err, os.ErrNotExist) {
		prev, err = nil, nil
	}
	if err != nil {
		return fmt.Errorf("previous manifest: %w", err)
	}
	gen := c.Generation
	if gen == 0 {
		claimed, err := HighestClaimedGeneration(c.CaseDir)
		if err != nil {
			return err
		}
		if gen, err = NextGeneration(prev, claimed); err != nil {
			return err
		}
	}
	dir := filepath.Join(c.CaseDir, GenerationDir(gen))
	// An exclusive directory claim rejects a duplicate generation.
	if err := os.Mkdir(dir, 0o755); err != nil {
		return fmt.Errorf("claim generation %d: %w", gen, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, ReportFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	self, err := selfIdentity(RoleMain, gen)
	if err != nil {
		return err
	}
	m := &mainRole{cfg: c, prefix: prefix, dir: dir, self: self,
		report: &reportWriter{w: f, generation: gen, now: time.Now}}
	if err := m.emit(Event{Kind: EventStart, Mode: c.LaunchMode, OS: osVersion(), Identity: &self}); err != nil {
		return err
	}
	if err := m.checkPrevious(prev); err != nil {
		return m.fatal("previous-generation", err)
	}
	if err := m.writeManifest(StageStart); err != nil {
		return m.fatal("manifest", err)
	}
	if err := m.createInner(); err != nil {
		return m.fatal("inner-job", err)
	}
	if err := m.launchEngine(); err != nil {
		return err
	}
	if err := m.awaitTree(); err != nil {
		return err
	}
	return m.serve()
}

func (m *mainRole) emit(e Event) error {
	return m.report.emit(e)
}

func (m *mainRole) fatal(op string, err error) error {
	f := failure(op, err)
	_ = m.emit(Event{Kind: EventFatal, Failure: f})
	return err
}

func (m *mainRole) writeManifest(stage string) error {
	ids := append([]Identity{m.self}, m.tree...)
	return WriteJSON(filepath.Join(m.cfg.CaseDir, ManifestFile), Manifest{
		Generation: m.self.Generation, Invocation: m.self.Invocation, Mode: m.cfg.LaunchMode,
		Stage: stage, Identities: ids,
	})
}

// checkPrevious runs before any work: every process of the previous
// generation must be provably gone. An inaccessible or live process fails.
func (m *mainRole) checkPrevious(prev *Manifest) error {
	if prev == nil {
		return m.emit(Event{Kind: EventPrevious, Note: "none"})
	}
	var checks []PreviousCheck
	var survivors int
	for _, id := range prev.Identities {
		c := CheckGone(id)
		if c.State == PreviousRunning || c.State == PreviousUnknown {
			survivors++
		}
		checks = append(checks, c)
	}
	if err := m.emit(Event{Kind: EventPrevious, Note: "generation " + strconv.Itoa(prev.Generation), Previous: checks}); err != nil {
		return err
	}
	if survivors > 0 {
		return fmt.Errorf("%d previous-generation processes are not proven gone", survivors)
	}
	return nil
}

func (m *mainRole) createInner() error {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	m.inner = h
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return fmt.Errorf("set inner kill-on-close: %w", err)
	}
	// MAIN owns the sole handle: never inheritable, never exported.
	if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return fmt.Errorf("clear inner inheritance: %w", err)
	}
	inner, err := m.queryInner()
	if err != nil {
		return err
	}
	if inner.Inheritable {
		return errors.New("inner job handle is inheritable")
	}
	if inner.LimitFlags != windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE {
		return fmt.Errorf("inner limit flags %#x", inner.LimitFlags)
	}
	return m.emit(Event{Kind: EventInnerJob, Inner: &inner})
}

func (m *mainRole) queryInner() (InnerJob, error) {
	out := InnerJob{Handle: uint64(m.inner)}
	var err error
	if out.LimitFlags, err = jobLimitFlags(m.inner); err != nil {
		return out, fmt.Errorf("query inner limits: %w", err)
	}
	if out.CPUControlFlags, err = jobCPUFlags(m.inner); err != nil {
		out.CPUQueryError = err.Error()
	}
	if out.Inheritable, err = handleInheritable(m.inner); err != nil {
		return out, fmt.Errorf("query inner handle: %w", err)
	}
	return out, nil
}

func (m *mainRole) launchEngine() error {
	probeValue := uint64(m.inner)
	var inherit []windows.Handle
	if m.cfg.Sensitivity == SensitivityInheritInner {
		var dup windows.Handle
		if err := windows.DuplicateHandle(windows.CurrentProcess(), m.inner, windows.CurrentProcess(), &dup, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return m.fatal("duplicate-inner", err)
		}
		inherit = []windows.Handle{dup}
		probeValue = uint64(dup)
		defer func() {
			// Only ENGINE's inherited copy may outlive creation.
			if err := windows.CloseHandle(dup); err == nil {
				_ = m.emit(Event{Kind: EventInnerClosed, Note: "local inheritable duplicate closed after ENGINE creation"})
			}
		}()
	}
	argv, err := selfArgv(m.prefix, EngineConfig{CaseDir: m.cfg.CaseDir, Generation: m.self.Generation, JobHandle: probeValue, Work: m.cfg.Work}.Args())
	if err != nil {
		return m.fatal("engine-argv", err)
	}
	var jobList []windows.Handle
	if m.cfg.LaunchMode == ModeJobList {
		jobList = []windows.Handle{m.inner}
	}
	pi, err := startProcess(argv, windows.CREATE_SUSPENDED, jobList, inherit)
	if err != nil {
		var unavailable *unavailableError
		if errors.As(err, &unavailable) {
			_ = m.emit(Event{Kind: EventUnqualified, OS: osVersion(), Failure: failure("job-list", err)})
			return err
		}
		return m.fatal("create-engine", err)
	}
	id, err := childIdentity(RoleEngine, pi, m.self)
	if err != nil {
		// The suspended child exists; terminate exactly it before failing.
		return m.abandon(&heldProcess{id: Identity{Role: RoleEngine, PID: pi.ProcessId, Generation: m.self.Generation}, process: pi.Process, thread: pi.Thread}, "engine-identity", err)
	}
	child := &heldProcess{id: id, process: pi.Process, thread: pi.Thread}
	m.engine = child
	if err := m.emit(Event{Kind: EventCreated, Identity: &id}); err != nil {
		return m.abandon(child, "report", err)
	}
	if m.cfg.Gate == GatePreAssign {
		return m.holdGate(child)
	}
	if m.cfg.LaunchMode == ModeAssign {
		if err := windows.AssignProcessToJobObject(m.inner, child.process); err != nil {
			return m.abandon(child, "assign-inner", err)
		}
		if err := m.emit(Event{Kind: EventAssigned, Identity: &id}); err != nil {
			return m.abandon(child, "report", err)
		}
	}
	in, err := IsProcessInJob(child.process, m.inner)
	if err != nil {
		return m.abandon(child, "verify-inner", err)
	}
	if !in {
		return m.abandon(child, "verify-inner", errors.New("ENGINE is not in the inner job before resume"))
	}
	if m.cfg.Gate == GatePreResume {
		return m.holdGate(child)
	}
	previous, err := windows.ResumeThread(child.thread)
	if err != nil {
		return m.abandon(child, "resume-engine", err)
	}
	if previous != 1 {
		return m.abandon(child, "resume-engine", fmt.Errorf("previous suspend count %d", previous))
	}
	if err := windows.CloseHandle(child.thread); err != nil {
		return m.fatal("close-engine-thread", err)
	}
	child.thread = 0
	return m.emit(Event{Kind: EventResumed, Identity: &id})
}

// abandon terminates a child that never ran. If the outcome is uncertain the
// handles stay owned until exit; the unit job still contains the child.
func (m *mainRole) abandon(child *heldProcess, op string, cause error) error {
	terr := windows.TerminateProcess(child.process, 1)
	state, werr := windows.WaitForSingleObject(child.process, 5000)
	if terr == nil && werr == nil && state == windows.WAIT_OBJECT_0 {
		_ = windows.CloseHandle(child.thread)
		_ = windows.CloseHandle(child.process)
		return m.fatal(op, fmt.Errorf("%w; unstarted child terminated", cause))
	}
	return m.fatal(op, fmt.Errorf("%w; unstarted child outcome unknown (terminate: %v, wait: %d %v)", cause, terr, state, werr))
}

// holdGate publishes the suspended child and waits to be killed.
func (m *mainRole) holdGate(child *heldProcess) error {
	in, err := IsProcessInJob(child.process, m.inner)
	if err != nil {
		return m.abandon(child, "gate-membership", err)
	}
	m.tree = []Identity{child.id}
	if err := m.writeManifest(StageGate); err != nil {
		return m.abandon(child, "manifest", err)
	}
	if err := m.emit(Event{Kind: EventGate, Gate: m.cfg.Gate, Tree: []Member{{Identity: child.id, InInner: in}}}); err != nil {
		return m.abandon(child, "report", err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func (m *mainRole) awaitTree() error {
	var engine RoleStatus
	if err := m.awaitStatus(RoleEngine, &engine); err != nil {
		return m.fatal("engine-status", err)
	}
	if !engine.Identity.Same(m.engine.id) || len(engine.Children) != 2 {
		return m.fatal("engine-status", errors.New("ENGINE status does not match its creation record"))
	}
	m.engine.id = engine.Identity
	m.engine.id.ParentPID, m.engine.id.ParentCreated = m.self.PID, m.self.Created
	members := []Member{{Identity: m.self}, {Identity: m.engine.id}}
	for i, role := range []string{RoleG1, RoleG2} {
		var leaf RoleStatus
		if err := m.awaitStatus(role, &leaf); err != nil {
			return m.fatal(role+"-status", err)
		}
		created := engine.Children[i]
		if created.Role != role || !leaf.Identity.Same(created) || created.ParentPID != engine.Identity.PID || created.ParentCreated != engine.Identity.Created {
			return m.fatal(role+"-status", errors.New("leaf status does not match ENGINE's creation record"))
		}
		h, err := openIdentity(leaf.Identity, windows.SYNCHRONIZE)
		if err != nil {
			return m.fatal(role+"-open", err)
		}
		id := leaf.Identity
		id.ParentPID, id.ParentCreated = created.ParentPID, created.ParentCreated
		m.leaves = append(m.leaves, &heldProcess{id: id, process: h})
		members = append(members, Member{Identity: id})
	}
	held := []windows.Handle{windows.CurrentProcess(), m.engine.process, m.leaves[0].process, m.leaves[1].process}
	ok := true
	for i := range members {
		in, err := IsProcessInJob(held[i], m.inner)
		if err != nil {
			return m.fatal("tree-membership", err)
		}
		members[i].InInner = in
		ok = ok && in == (i > 0)
		if b, err := PrivateBytes(held[i]); err == nil {
			members[i].PrivateBytes = b
		}
	}
	inner, err := m.queryInner()
	if err != nil {
		return m.fatal("inner-query", err)
	}
	m.tree = []Identity{members[1].Identity, members[2].Identity, members[3].Identity}
	if err := m.writeManifest(StageTree); err != nil {
		return m.fatal("manifest", err)
	}
	if err := m.emit(Event{Kind: EventTree, Tree: members, Inner: &inner}); err != nil {
		return err
	}
	if !ok {
		return m.fatal("tree-membership", errors.New("inner membership differs from MAIN outside, descendants inside"))
	}
	return nil
}

// awaitStatus waits for a role's self-written status while ENGINE lives.
func (m *mainRole) awaitStatus(role string, v *RoleStatus) error {
	path := filepath.Join(m.dir, StatusFile(role))
	deadline := time.Now().Add(statusTimeout)
	for {
		err := ReadJSON(path, v)
		if err == nil {
			return v.Identity.validate(true)
		}
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) {
			return err
		}
		if done, _ := signaled(m.engine.process); done {
			return errors.New("ENGINE exited before the tree was ready")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s status: %w", role, err)
		}
		time.Sleep(pollInterval)
	}
}

// serve handles enumerated commands and the cooperative stop request.
func (m *mainRole) serve() error {
	deadline := time.Now().Add(observedTimeout)
	loop := &CommandServer{Dir: m.dir, Role: RoleMain, Handle: m.command}
	stopPath := filepath.Join(m.dir, StopRequestFile)
	for {
		if !m.ready && time.Now().After(deadline) {
			return m.fatal("observer", errors.New("observer acknowledgment timed out"))
		}
		if _, err := loop.Poll(); err != nil {
			return m.fatal("command", err)
		}
		var req StopRequest
		var pathErr *fs.PathError
		switch err := ReadJSON(stopPath, &req); {
		case err == nil:
			if done, err := m.stopRequested(req); done || err != nil {
				return err
			}
		case !errors.As(err, &pathErr):
			return m.fatal("stop-request", err)
		}
		time.Sleep(pollInterval)
	}
}

func (m *mainRole) stopRequested(req StopRequest) (bool, error) {
	if m.cfg.OnStop == OnStopIgnore {
		if !m.ignored {
			m.ignored = true
			return false, m.emit(Event{Kind: EventStopIgnored, Note: "helper pid " + strconv.FormatUint(uint64(req.Helper.PID), 10)})
		}
		return false, nil
	}
	if err := m.emit(Event{Kind: EventStopRequested, Note: "helper pid " + strconv.FormatUint(uint64(req.Helper.PID), 10)}); err != nil {
		return true, err
	}
	if m.inner != 0 {
		if err := m.closeInner("stop request"); err != nil {
			return true, m.fatal("stop-close-inner", err)
		}
	}
	deadline := time.Now().Add(stopWaitTimeout)
	for _, p := range append([]*heldProcess{m.engine}, m.leaves...) {
		for {
			done, err := signaled(p.process)
			if err != nil {
				return true, m.fatal("stop-wait", err)
			}
			if done {
				break
			}
			if time.Now().After(deadline) {
				return true, m.fatal("stop-wait", fmt.Errorf("%s did not exit after the inner job closed", p.id.Role))
			}
			time.Sleep(pollInterval)
		}
	}
	return true, m.emit(Event{Kind: EventStopComplete, Note: "engine tree exited"})
}

func (m *mainRole) closeInner(reason string) error {
	if m.inner == 0 {
		return errors.New("inner job already closed")
	}
	if err := windows.CloseHandle(m.inner); err != nil {
		return err
	}
	m.inner = 0
	return m.emit(Event{Kind: EventInnerClosed, Note: reason})
}

func (m *mainRole) command(cmd Command) Ack {
	ack := Ack{Seq: cmd.Seq, Verb: cmd.Verb, OK: true}
	_ = m.emit(Event{Kind: EventCommand, Note: cmd.Verb + " " + strconv.Itoa(cmd.Seq)})
	switch cmd.Verb {
	case VerbObserved:
		if !m.ready {
			if err := m.emit(Event{Kind: EventReady}); err != nil {
				return failedAck(ack, "ready", err)
			}
			m.ready = true
		}
	case VerbPing:
	case VerbCloseInner:
		if err := m.closeInner("observer command"); err != nil {
			return failedAck(ack, "close-inner", err)
		}
		ack.Closed = 1
	case VerbSetInnerBreakaway:
		flags, err := m.setInnerBreakaway(cmd.InnerBreakaway)
		if err != nil {
			return failedAck(ack, "set-inner-breakaway", err)
		}
		ack.LimitFlags = flags
	case VerbCheckInner:
		if m.inner == 0 {
			return failedAck(ack, "check-inner", errors.New("inner job closed"))
		}
		h, err := openIdentity(Identity{Role: RoleProbe, PID: cmd.PID, Created: cmd.Created}, 0)
		if err != nil {
			return failedAck(ack, "check-inner", err)
		}
		in, err := IsProcessInJob(h, m.inner)
		_ = windows.CloseHandle(h)
		if err != nil {
			return failedAck(ack, "check-inner", err)
		}
		ack.InInner = &in
	case VerbProbe:
		probe, held := createProbe(m.self, m.prefix, m.cfg.CaseDir, cmd.Breakaway)
		if held != nil {
			m.probes = append(m.probes, held)
		}
		ack.Probe = &probe
	}
	return ack
}

func (m *mainRole) setInnerBreakaway(mode string) (uint32, error) {
	if m.inner == 0 {
		return 0, errors.New("inner job closed")
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(m.inner, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return 0, err
	}
	if mode == InnerBreakawayExplicit {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	} else {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK
	}
	if _, err := windows.SetInformationJobObject(m.inner, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, err
	}
	return jobLimitFlags(m.inner)
}

func failedAck(ack Ack, op string, err error) Ack {
	ack.OK = false
	ack.Failure = failure(op, err)
	return ack
}

// createProbe attempts one extra child, suspended. It is never resumed: its
// job membership is fixed at creation and the observer inspects it as is.
func createProbe(creator Identity, prefix []string, caseDir string, breakaway bool) (ProbeResult, *heldProcess) {
	res := ProbeResult{Creator: creator.Role, Breakaway: breakaway}
	argv, err := selfArgv(prefix, LeafConfig{CaseDir: caseDir, Generation: creator.Generation, Role: RoleProbe, JobHandle: 1, Work: WorkIdle}.Args())
	if err != nil {
		res.Failure = failure("probe-argv", err)
		return res, nil
	}
	flags := uint32(windows.CREATE_SUSPENDED)
	if breakaway {
		flags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	pi, err := startProcess(argv, flags, nil, nil)
	if err != nil {
		res.Failure = failure("CreateProcess", err)
		return res, nil
	}
	res.Created = true
	held := &heldProcess{process: pi.Process, thread: pi.Thread}
	id, err := childIdentity(RoleProbe, pi, creator)
	if err != nil {
		res.Failure = failure("probe-identity", err)
		held.id = Identity{Role: RoleProbe, PID: pi.ProcessId, Generation: creator.Generation}
		return res, held
	}
	held.id = id
	res.Identity = &id
	if in, err := IsProcessInJob(pi.Process, 0); err != nil {
		res.Failure = failure("IsProcessInJob", err)
	} else {
		res.InAnyJob = in
	}
	return res, held
}

// worker is the shared ENGINE/leaf runtime: status, commands and work.
type worker struct {
	dir     string
	prefix  []string
	caseDir string
	self    Identity
	work    string
	handle  HandleProbe
	// Creation handles of G1/G2 and suspended probes stay owned until exit.
	held    []*heldProcess
	started atomic.Bool
}

func (w *worker) command(cmd Command) Ack {
	ack := Ack{Seq: cmd.Seq, Verb: cmd.Verb, OK: true}
	switch cmd.Verb {
	case VerbPing:
	case VerbProbe:
		probe, held := createProbe(w.self, w.prefix, w.caseDir, cmd.Breakaway)
		if held != nil {
			w.held = append(w.held, held)
		}
		ack.Probe = &probe
	case VerbStartWork:
		if w.started.CompareAndSwap(false, true) {
			go w.run()
		}
	case VerbCloseInherited:
		if !w.handle.IsJob {
			return failedAck(ack, "close-inherited", errors.New("no inherited job handle"))
		}
		// Acknowledge first: if this is the job's last handle, closing it
		// terminates this process together with the rest of the inner job.
		if err := WriteJSON(filepath.Join(w.dir, AckFile(w.self.Role, cmd.Seq)), ack); err != nil {
			return failedAck(ack, "close-inherited", err)
		}
		if err := windows.CloseHandle(windows.Handle(w.handle.Value)); err != nil {
			return failedAck(ack, "close-inherited", err)
		}
		w.handle.IsJob = false
		ack.Closed = 1
	}
	return ack
}

func (w *worker) run() {
	status := WorkStatus{Role: w.self.Role, Work: w.work}
	path := filepath.Join(w.dir, WorkFile(w.self.Role))
	switch w.work {
	case WorkCPU:
		for range goruntime.NumCPU() {
			go spin()
		}
		_ = WriteJSON(path, status)
	case WorkCommit:
		for status.CommittedBytes < CommitPerProcess {
			addr, err := windows.VirtualAlloc(0, CommitStep, windows.MEM_RESERVE|windows.MEM_COMMIT, windows.PAGE_READWRITE)
			if err != nil {
				status.Failure = failure("VirtualAlloc", err)
				break
			}
			// Touch every page so the commitment is also resident.
			page := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
			b := unsafe.Slice((*byte)(page), CommitStep)
			for i := 0; i < len(b); i += 4096 {
				b[i] = 1
			}
			status.CommittedBytes += CommitStep
			_ = WriteJSON(path, status)
		}
		_ = WriteJSON(path, status)
	default:
		_ = WriteJSON(path, status)
	}
}

var spinSink atomic.Uint64

func spin() {
	var n uint64
	for {
		n++
		if n&0xfffff == 0 {
			spinSink.Store(n)
		}
	}
}

func (w *worker) serve() error {
	loop := &CommandServer{Dir: w.dir, Role: w.self.Role, Handle: w.command}
	for {
		if _, err := loop.Poll(); err != nil {
			return err
		}
		time.Sleep(pollInterval)
	}
}

func newWorker(caseDir string, generation int, role string, handle uint64, work string, prefix []string) (*worker, error) {
	// The inheritance probe runs first, before this process creates anything.
	probe := probeHandle(handle)
	self, err := selfIdentity(role, generation)
	if err != nil {
		return nil, err
	}
	return &worker{dir: filepath.Join(caseDir, GenerationDir(generation)), prefix: prefix, caseDir: caseDir,
		self: self, work: work, handle: probe}, nil
}

func (w *worker) writeStatus(children []Identity) error {
	st := RoleStatus{Identity: w.self, Children: children, HandleProbe: w.handle}
	if b, err := PrivateBytes(windows.CurrentProcess()); err == nil {
		st.PrivateBytes = b
	}
	return WriteJSON(filepath.Join(w.dir, StatusFile(w.self.Role)), st)
}

func runEngine(c EngineConfig, prefix []string) error {
	w, err := newWorker(c.CaseDir, c.Generation, RoleEngine, c.JobHandle, c.Work, prefix)
	if err != nil {
		return err
	}
	var children []Identity
	for _, role := range []string{RoleG1, RoleG2} {
		argv, err := selfArgv(prefix, LeafConfig{CaseDir: c.CaseDir, Generation: c.Generation, Role: role, JobHandle: c.JobHandle, Work: c.Work}.Args())
		if err != nil {
			return err
		}
		// Ordinary children: no breakaway and no inherited handles.
		pi, err := startProcess(argv, 0, nil, nil)
		if err != nil {
			return fmt.Errorf("create %s: %w", role, err)
		}
		_ = windows.CloseHandle(pi.Thread)
		id, err := childIdentity(role, pi, w.self)
		if err != nil {
			return err
		}
		// Keep the creation handle so the PID cannot be reused while running.
		w.held = append(w.held, &heldProcess{id: id, process: pi.Process})
		children = append(children, id)
	}
	if err := w.writeStatus(children); err != nil {
		return err
	}
	return w.serve()
}

func runLeaf(c LeafConfig, prefix []string) error {
	if c.Role == RoleProbe {
		// Probes are inspected while suspended; if one ever runs, it idles.
		for {
			time.Sleep(time.Hour)
		}
	}
	w, err := newWorker(c.CaseDir, c.Generation, c.Role, c.JobHandle, c.Work, prefix)
	if err != nil {
		return err
	}
	if err := w.writeStatus(nil); err != nil {
		return err
	}
	return w.serve()
}

// runStop is the ExecStop helper: verify MAINPID against the manifest, ask
// MAIN to stop, then wait for it (cooperative) or hang until forced cleanup.
func runStop(c StopConfig) error {
	man, err := ReadManifest(c.CaseDir)
	if err != nil {
		return err
	}
	self, err := selfIdentity(RoleStop, man.Generation)
	if err != nil {
		return err
	}
	dir := filepath.Join(c.CaseDir, GenerationDir(man.Generation))
	status := StopHelperStatus{Identity: self, Behavior: c.Behavior}
	path := filepath.Join(dir, StopHelperFile(self.PID))
	main := man.Identities[0]
	if v, err := strconv.ParseUint(os.Getenv("MAINPID"), 10, 32); err == nil {
		status.MainPID = uint32(v)
	}
	var h windows.Handle
	if status.MainPID == main.PID {
		h, err = openIdentity(main, windows.SYNCHRONIZE)
		if err == nil {
			defer windows.CloseHandle(h)
			status.MainMatched = true
		}
	}
	if !status.MainMatched {
		status.Failure = &Failure{Op: "mainpid", Message: fmt.Sprintf("MAINPID %d does not name generation %d MAIN %d", status.MainPID, man.Generation, main.PID)}
		_ = WriteJSON(path, status)
		return status.Failure
	}
	if err := WriteJSON(path, status); err != nil {
		return err
	}
	if err := WriteJSON(filepath.Join(dir, StopRequestFile), StopRequest{Helper: self, MainPID: status.MainPID}); err != nil {
		return err
	}
	if c.Behavior == StopHang {
		for {
			time.Sleep(time.Hour)
		}
	}
	state, err := windows.WaitForSingleObject(h, uint32(helperMainWait/time.Millisecond))
	if err != nil || state != windows.WAIT_OBJECT_0 {
		status.Failure = &Failure{Op: "wait-main", Message: fmt.Sprintf("state %d: %v", state, err)}
		_ = WriteJSON(path, status)
		return status.Failure
	}
	status.MainExited = true
	return WriteJSON(path, status)
}
