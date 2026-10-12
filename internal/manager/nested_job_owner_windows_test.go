//go:build windows

package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/runtime/runtimetest/nestedjob"
	"golang.org/x/sys/windows"
)

// Native-owner lane for the manager cases of #265. The SYSTEM owner is a
// Manager in this test process. The headless owner is the same isolated
// Manager running inside a genuine S4U process of a dedicated, logged-off
// local standard account, launched through the production headless path.
// It follows the user-manager entry pattern (its own daemon job, UserScope,
// an isolated base directory), serves no control endpoint and answers only
// enumerated questions; this test process observes with held handles.

const nestedManagerOwnerSelector = winunitdHelperArgPrefix + "nested-manager-owner"

const nestedAgentTimeout = 60 * time.Second

// nestedBroker is the SYSTEM root for headless launches. Self-assignment
// lasts for the runner's lifetime; TestMain closes it after every case has
// drained its own work.
var nestedBroker struct {
	once sync.Once
	job  *runtime.DaemonJob
	err  error
}

func nestedBrokerRoot() (*runtime.DaemonJob, error) {
	nestedBroker.once.Do(func() {
		nestedBroker.job, nestedBroker.err = runtime.OpenBrokerJob()
		if nestedBroker.err == nil {
			nestedBroker.err = nestedBroker.job.AssignSelf()
		}
	})
	return nestedBroker.job, nestedBroker.err
}

func closeNestedBroker() error {
	if nestedBroker.job == nil {
		return nil
	}
	return nestedBroker.job.Close()
}

type managerOwner interface {
	// start reloads and starts nested.service.
	start(t *testing.T)
	// stopUnit stops nested.service and returns the owner's outcome.
	stopUnit(t *testing.T) stopOutcome
	// stopAsync requests a stop now; the returned function waits until
	// deadline for the outcome and reports a stop that had not finished.
	stopAsync(t *testing.T) func(deadline time.Time) (stopOutcome, error)
	// inspect returns a validated view; a failed observation fails the test.
	inspect(t *testing.T) nestedjob.ManagerView
	// inUnitJob checks an exact, observer-held process in the unit job.
	inUnitJob(t *testing.T, h *nestedjob.Held) bool
	// expectDrained arms the replacement-launch observation for held.
	expectDrained(t *testing.T, held []*nestedjob.Held)
	token() *nestedjob.TokenContext
	// close stops the unit, releases the manager and, for an agent,
	// confirms that the agent process exited.
	close() error
}

// stopOutcome is one unit stop as its owner saw it: the time from the
// stop request to Stop's return, measured where Stop ran, the manager's
// result and the view taken the moment Stop returned.
type stopOutcome struct {
	elapsed time.Duration
	err     error
	view    nestedjob.ManagerView
}

// nestedView answers inspect for a manager and its observing launcher. A
// failed status or limits query is reported as InspectError; a unit with
// no process has no job, which is not an error.
func nestedView(m *Manager, launch *nestedLauncher) nestedjob.ManagerView {
	var v nestedjob.ManagerView
	st, err := m.Status(nestedUnitName)
	switch {
	case err != nil:
		v.InspectError = nestedjob.FailureOf("status", err)
		return v
	case st.Unit == nil:
		v.InspectError = &nestedjob.Failure{Op: "status", Message: "no unit status"}
		return v
	}
	u := st.Unit
	v.ActiveState, v.Reason, v.Error, v.InvocationID = u.ActiveState, u.Reason, u.Error, u.InvocationID
	v.TerminationUncertain, v.MainPID = u.TerminationUncertain, u.MainPID
	v.CPUQuota, v.WindowsCPUQuota = u.CPUQuota, u.WindowsCPUQuota
	m.mu.Lock()
	v.StopHelpers = len(m.stopHelpers)
	proc := m.procOfLocked(nestedUnitName)
	m.mu.Unlock()
	if proc != nil {
		lim, err := proc.Job().QueryLimits()
		if err != nil {
			v.InspectError = nestedjob.FailureOf("query-limits", err)
			return v
		}
		v.HasJob = true
		v.JobMemory, v.PeakJobMemory, v.LimitFlags = lim.JobMemory, lim.PeakJobMemory, lim.LimitFlags
		v.CPURate, v.CPUControlFlags = lim.CPURate, lim.CPUControlFlags
	}
	// After the slot count: a released slot is observed before its helper.
	v.Helpers = launch.helperStates()
	v.Launches, v.Violations = launch.state()
	return v
}

// nestedMember checks an exact held process against the manager's current
// unit job. The handle keeps the PID reserved while it is unsignaled.
func nestedMember(m *Manager, h windows.Handle, pid uint32) (bool, error) {
	if done, err := signaledHandle(h); err != nil || done {
		return false, fmt.Errorf("process %d is not running: %v", pid, err)
	}
	m.mu.Lock()
	proc := m.procOfLocked(nestedUnitName)
	m.mu.Unlock()
	if proc == nil {
		return false, errors.New("manager owns no process for the nested unit")
	}
	return proc.Job().Contains(int(pid))
}

func signaledHandle(h windows.Handle) (bool, error) {
	state, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false, err
	}
	return state == windows.WAIT_OBJECT_0, nil
}

// localManagerOwner runs the Manager in this test process.
type localManagerOwner struct {
	m      *Manager
	launch *nestedLauncher
	tok    *nestedjob.TokenContext
}

func newLocalManagerOwner(t *testing.T, base string, launch *nestedLauncher) *localManagerOwner {
	t.Helper()
	tok, err := nestedjob.CurrentTokenContext()
	if err != nil {
		t.Fatal(err)
	}
	launch.inner = runtime.DefaultLauncher()
	m, err := New(Config{BaseDir: base, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	return &localManagerOwner{m: m, launch: launch, tok: tok}
}

func (o *localManagerOwner) start(t *testing.T) {
	t.Helper()
	if _, err := o.m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := o.m.Start(context.Background(), nestedUnitName); err != nil {
		t.Fatal(err)
	}
}

func (o *localManagerOwner) stopUnit(t *testing.T) stopOutcome {
	t.Helper()
	out, err := o.stopAsync(t)(time.Now().Add(nestedAgentTimeout))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (o *localManagerOwner) stopAsync(*testing.T) func(time.Time) (stopOutcome, error) {
	requested := time.Now()
	done := make(chan stopOutcome, 1)
	go func() {
		_, err := o.m.Stop(nestedUnitName)
		out := stopOutcome{elapsed: time.Since(requested), err: err}
		out.view = nestedView(o.m, o.launch)
		done <- out
	}()
	return func(deadline time.Time) (stopOutcome, error) {
		select {
		case out := <-done:
			return out, nil
		default:
		}
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		select {
		case out := <-done:
			return out, nil
		case <-timer.C:
			return stopOutcome{}, errors.New("stop did not finish by its deadline")
		}
	}
}

func (o *localManagerOwner) inspect(t *testing.T) nestedjob.ManagerView {
	t.Helper()
	v := nestedView(o.m, o.launch)
	if err := v.Validate(); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return v
}

func (o *localManagerOwner) inUnitJob(t *testing.T, h *nestedjob.Held) bool {
	t.Helper()
	in, err := nestedMember(o.m, h.Handle, h.ID.PID)
	if err != nil {
		t.Fatalf("unit-job membership of %s: %v", h.ID.Role, err)
	}
	return in
}

func (o *localManagerOwner) expectDrained(_ *testing.T, held []*nestedjob.Held) {
	o.launch.expectDrained(held)
}

func (o *localManagerOwner) token() *nestedjob.TokenContext { return o.tok }

func (o *localManagerOwner) close() error {
	o.launch.release()
	_, err := o.m.Stop(nestedUnitName)
	o.m.Close()
	return errors.Join(err, o.launch.close())
}

// agentManagerOwner drives a manager-owner agent in a genuine S4U process.
type agentManagerOwner struct {
	dir  string
	obs  *nestedjob.Observer
	proc runtime.UserManagerProc
	tok  *nestedjob.TokenContext
}

func newAgentManagerOwner(t *testing.T, sid, base string) *agentManagerOwner {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		t.Fatal("the headless owner lane needs a SYSTEM runner")
	}
	tok, err := runtime.ObtainLingerToken(runtime.LingerRecord{SID: sid})
	if tok != nil {
		t.Cleanup(func() {
			if err := tok.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	if tok.Info.SID != sid || tok.Source != runtime.LingerTokenPathS4U {
		t.Fatal("a genuine S4U token for the configured account is required")
	}
	broker, err := nestedBrokerRoot()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "owner")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	proc, err := runtime.StartUserManager(runtime.UserManagerSpec{
		SID: sid, Token: tok, Exe: exe, Daemon: broker, LoadProfile: true,
		ExtraArgs: []string{nestedManagerOwnerSelector, "--agent-dir", dir, "--base-dir", base},
	})
	// A returned process is this test's to confirm gone, whatever happens
	// next: Kill confirms the agent's whole tree exited and is idempotent
	// after a successful close.
	if proc != nil {
		t.Cleanup(func() {
			if err := proc.Kill(); err != nil {
				t.Errorf("manager owner agent exit unconfirmed: %v", err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	a := &agentManagerOwner{dir: dir, obs: nestedjob.NewObserver(base), proc: proc}
	var self nestedjob.Identity
	deadline := time.Now().Add(nestedAgentTimeout)
	for {
		err := nestedjob.ReadJSON(filepath.Join(dir, "owner.json"), &self)
		if err == nil {
			break
		}
		if !proc.Alive() || time.Now().After(deadline) {
			t.Fatalf("manager owner agent did not start: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.tok = &nestedjob.TokenContext{SID: self.SID, Session: self.Session, Elevated: self.Elevated, Source: nestedjob.TokenS4U}
	if self.SID != sid || self.Session != 0 || self.Elevated || self.PID != uint32(proc.PID()) {
		t.Fatalf("manager owner agent is not the headless account in session zero: account %t session %d elevated %t own pid %t",
			self.SID == sid, self.Session, self.Elevated, self.PID == uint32(proc.PID()))
	}
	return a
}

func (a *agentManagerOwner) command(t *testing.T, cmd nestedjob.Command) nestedjob.Ack {
	t.Helper()
	ack, err := a.obs.CommandIn(a.dir, nestedjob.RoleManagerOwner, cmd, nestedAgentTimeout)
	if err != nil {
		t.Fatalf("manager owner agent %s: %v", cmd.Verb, err)
	}
	return ack
}

func (a *agentManagerOwner) start(t *testing.T) {
	t.Helper()
	a.command(t, nestedjob.Command{Verb: nestedjob.VerbStart})
}

// stopResult maps an agent's stop-unit acknowledgment to the outcome. A
// negative acknowledgment carries the manager's stop error; the agent
// times the stop and takes the view where Stop ran.
func stopResult(ack nestedjob.Ack, err error) (stopOutcome, error) {
	if ack.Seq == 0 {
		return stopOutcome{}, fmt.Errorf("manager owner agent did not answer the stop: %w", err)
	}
	out := stopOutcome{elapsed: time.Duration(ack.ElapsedMS) * time.Millisecond}
	if !ack.OK && ack.Failure != nil {
		out.err = errors.New(ack.Failure.Message)
	}
	if ack.Manager == nil {
		return out, errors.New("manager owner agent answered the stop without a view")
	}
	out.view = *ack.Manager
	return out, nil
}

func (a *agentManagerOwner) stopUnit(t *testing.T) stopOutcome {
	t.Helper()
	out, err := a.stopAsync(t)(time.Now().Add(nestedAgentTimeout))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (a *agentManagerOwner) stopAsync(t *testing.T) func(time.Time) (stopOutcome, error) {
	t.Helper()
	seq, err := a.obs.SendIn(a.dir, nestedjob.RoleManagerOwner, nestedjob.Command{Verb: nestedjob.VerbStopUnit})
	if err != nil {
		t.Fatal(err)
	}
	return func(deadline time.Time) (stopOutcome, error) {
		return stopResult(a.obs.AwaitAck(a.dir, nestedjob.RoleManagerOwner, seq, time.Until(deadline)))
	}
}

func (a *agentManagerOwner) inspect(t *testing.T) nestedjob.ManagerView {
	t.Helper()
	ack := a.command(t, nestedjob.Command{Verb: nestedjob.VerbInspect})
	if ack.Manager == nil {
		t.Fatal("manager owner agent answered without a view")
	}
	if err := ack.Manager.Validate(); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return *ack.Manager
}

func (a *agentManagerOwner) inUnitJob(t *testing.T, h *nestedjob.Held) bool {
	t.Helper()
	ack := a.command(t, nestedjob.Command{Verb: nestedjob.VerbInUnitJob, PID: h.ID.PID, Created: h.ID.Created})
	if ack.InUnitJob == nil {
		t.Fatal("manager owner agent answered without membership")
	}
	return *ack.InUnitJob
}

func (a *agentManagerOwner) expectDrained(t *testing.T, held []*nestedjob.Held) {
	t.Helper()
	ids := make([]nestedjob.Identity, len(held))
	for i, h := range held {
		ids[i] = h.ID
	}
	a.command(t, nestedjob.Command{Verb: nestedjob.VerbExpectDrained, Held: ids})
}

func (a *agentManagerOwner) token() *nestedjob.TokenContext { return a.tok }

func (a *agentManagerOwner) close() error {
	_, exitErr := a.obs.CommandIn(a.dir, nestedjob.RoleManagerOwner, nestedjob.Command{Verb: nestedjob.VerbExit}, nestedAgentTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), nestedAgentTimeout)
	defer cancel()
	waitErr := a.proc.Wait(ctx)
	if exitErr == nil && waitErr == nil && !a.proc.Alive() {
		return a.proc.Kill() // releases handles; the process has exited
	}
	return errors.Join(exitErr, waitErr, a.proc.Kill(), errors.New("manager owner agent did not exit after its exit command"))
}

// newManagerOwner selects the owner for identity and its base directory,
// or skips an unconfigured headless lane with the reason.
func newManagerOwner(t *testing.T, identity, mode string, launch *nestedLauncher) (managerOwner, string, nestedjob.MainConfig) {
	t.Helper()
	switch identity {
	case nestedjob.IdentitySystem:
		base, cfg := nestedBase(t, mode, "")
		return newLocalManagerOwner(t, base, launch), base, cfg
	case nestedjob.IdentityHeadless:
		sid, err := nestedjob.HeadlessSID()
		if err != nil {
			t.Fatal(err)
		}
		if sid == "" {
			t.Skip("headless owner lane not configured: set " + nestedjob.EnvHeadlessSID + " to a dedicated logged-off local standard account and run as SYSTEM")
		}
		base, cfg := nestedBase(t, mode, sid)
		return newAgentManagerOwner(t, sid, base), base, cfg
	}
	t.Fatalf("identity %q", identity)
	return nil, "", nestedjob.MainConfig{}
}

// nestedManagerAgentMain runs the agent and leaves any failure in its
// directory: a headless process has no console for standard error.
func nestedManagerAgentMain(args []string) int {
	err := runNestedManagerAgent(args)
	if err == nil {
		return 0
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--agent-dir" && filepath.IsAbs(args[i+1]) {
			_ = os.WriteFile(filepath.Join(args[i+1], "agent-error.txt"), []byte(err.Error()+"\n"), 0o644)
		}
	}
	return 1
}

// runNestedManagerAgent is the agent process: the user-manager entry
// pattern without its control endpoint, one nested unit, and enumerated
// answers. It drains the unit and closes its manager and job on exit.
func runNestedManagerAgent(args []string) error {
	var dir, base string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--agent-dir":
			dir = args[i+1]
		case "--base-dir":
			base = args[i+1]
		}
	}
	if !filepath.IsAbs(dir) || !filepath.IsAbs(base) {
		return errors.New("manager owner agent needs --agent-dir and --base-dir")
	}
	info, err := runtime.CurrentUserInfo()
	if err != nil {
		return err
	}
	if err := runtime.ApplyUserEnv(info); err != nil {
		return err
	}
	if err := runtime.SetHeadlessUserWorkingDirectory(info.Profile); err != nil {
		return err
	}
	if err := runtime.EnsureDataDirs(base); err != nil {
		return err
	}
	job, err := runtime.OpenDaemonJob()
	if err != nil {
		return err
	}
	if err := job.AssignSelf(); err != nil {
		return errors.Join(err, job.Close())
	}
	launch := &nestedLauncher{inner: runtime.NewLauncher(job)}
	m, err := New(Config{BaseDir: base, Daemon: job, UserScope: true, NotifySID: info.SID, Launch: launch,
		HasInteractiveSession: func() bool { return false }})
	if err != nil {
		return errors.Join(err, job.Close())
	}
	obs := nestedjob.NewObserver(base)
	self, err := nestedjob.SelfIdentity(nestedjob.RoleManagerOwner, 0)
	if err != nil {
		return err
	}
	exiting := false
	server := &nestedjob.CommandServer{Dir: dir, Role: nestedjob.RoleManagerOwner, Handle: func(cmd nestedjob.Command) nestedjob.Ack {
		ack := nestedjob.Ack{Seq: cmd.Seq, Verb: cmd.Verb, OK: true}
		fail := func(err error) nestedjob.Ack {
			ack.OK = false
			ack.Failure = &nestedjob.Failure{Op: cmd.Verb, Message: err.Error()}
			return ack
		}
		switch cmd.Verb {
		case nestedjob.VerbStart:
			if _, err := m.Reload(); err != nil {
				return fail(err)
			}
			if _, err := m.Start(context.Background(), nestedUnitName); err != nil {
				return fail(err)
			}
		case nestedjob.VerbStopUnit:
			started := time.Now()
			_, err := m.Stop(nestedUnitName)
			ack.ElapsedMS = time.Since(started).Milliseconds()
			v := nestedView(m, launch)
			ack.Manager = &v
			if err != nil {
				return fail(err)
			}
		case nestedjob.VerbInspect:
			v := nestedView(m, launch)
			ack.Manager = &v
		case nestedjob.VerbInUnitJob:
			h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, cmd.PID)
			if err != nil {
				return fail(err)
			}
			defer windows.CloseHandle(h)
			if created, err := nestedjob.CreationTime(h); err != nil || created != cmd.Created {
				return fail(fmt.Errorf("pid %d is not the observed process: %v", cmd.PID, err))
			}
			in, err := nestedMember(m, h, cmd.PID)
			if err != nil {
				return fail(err)
			}
			ack.InUnitJob = &in
		case nestedjob.VerbExpectDrained:
			var held []*nestedjob.Held
			for _, id := range cmd.Held {
				h, err := obs.Hold(id, 0)
				if err != nil {
					return fail(err)
				}
				held = append(held, h)
			}
			launch.expectDrained(held)
		case nestedjob.VerbExit:
			if _, err := m.Stop(nestedUnitName); err != nil {
				return fail(err)
			}
			exiting = true
		}
		return ack
	}}
	if err := nestedjob.WriteJSON(filepath.Join(dir, "owner.json"), self); err != nil {
		return err
	}
	idle := time.Now().Add(10 * time.Minute)
	var serveErr error
	for !exiting {
		handled, err := server.Poll()
		if err != nil {
			serveErr = err
			break
		}
		if handled {
			idle = time.Now().Add(10 * time.Minute)
		} else if time.Now().After(idle) {
			serveErr = errors.New("manager owner agent idle timeout")
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !exiting {
		_, err := m.Stop(nestedUnitName)
		serveErr = errors.Join(serveErr, err)
	}
	m.Close()
	return errors.Join(serveErr, launch.close(), obs.Close(), job.Close())
}
