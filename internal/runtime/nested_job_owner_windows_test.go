//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime/runtimetest/nestedjob"
	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows"
)

// Native-owner lane for workload-created nested jobs (#265). The process that
// creates the unit job is the owner; only it can query membership in that
// particular job. The SYSTEM lane's owner is this test process. The headless
// lane's owner is an agent in a genuine S4U process of a dedicated, logged-off
// local standard account, launched through the production headless path;
// this test process stays the SYSTEM observer and never holds a job handle.

// nestedOwnerSelector runs the owner agent instead of the test suite.
const nestedOwnerSelector = winunitdHelperArgPrefix + "nested-owner"

type nestedOwner interface {
	// launch starts MAIN as a Type=simple unit and returns its identity.
	launch(t *testing.T, cfg nestedjob.MainConfig) nestedjob.Identity
	// inUnitJob checks an exact, observer-held process in the unit job.
	inUnitJob(t *testing.T, h *nestedjob.Held) bool
	pids(t *testing.T) []int
	// stop stops the unit job and waits for its processes, like a unit stop.
	stop(timeout time.Duration) error
	token() *nestedjob.TokenContext
	// close releases the owner; for an agent it confirms the agent exited.
	close() error
}

// localOwner owns the unit job in this test process.
type localOwner struct {
	proc *winProc
	tok  *nestedjob.TokenContext
}

func newLocalOwner(t *testing.T) *localOwner {
	t.Helper()
	tok, err := nestedjob.CurrentTokenContext()
	if err != nil {
		t.Fatal(err)
	}
	return &localOwner{tok: tok}
}

func (o *localOwner) launch(t *testing.T, cfg nestedjob.MainConfig) nestedjob.Identity {
	t.Helper()
	argv := append([]string{testAbs(t), nestedjob.HelperSelector}, cfg.Args()...)
	p, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Unit: "nested.service", Type: unit.TypeSimple, Argv: argv, Dir: cfg.CaseDir, Env: helperEnv(),
	})
	if err != nil {
		t.Fatal(err)
	}
	o.proc = p.(*winProc)
	go drainNestedOutput(p.Stderr(), filepath.Join(cfg.CaseDir, "main-stderr.txt"))
	created, err := nestedjob.CreationTime(o.proc.process)
	if err != nil {
		t.Fatal(err)
	}
	return nestedjob.Identity{Role: nestedjob.RoleMain, PID: uint32(p.PID()), Created: created}
}

func (o *localOwner) inUnitJob(t *testing.T, h *nestedjob.Held) bool {
	t.Helper()
	in, err := o.proc.jobMember(h.Handle)
	if err != nil {
		t.Fatalf("unit-job membership of %s: %v", h.ID.Role, err)
	}
	return in
}

func (o *localOwner) pids(t *testing.T) []int {
	t.Helper()
	ids, err := o.proc.job.PIDs()
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func (o *localOwner) stop(timeout time.Duration) error {
	if o.proc == nil {
		return nil
	}
	return o.proc.Stop(timeout)
}

func (o *localOwner) token() *nestedjob.TokenContext { return o.tok }
func (o *localOwner) close() error                   { return o.stop(5 * time.Second) }

// jobMember checks an exact process handle against this unit's own job.
func (p *winProc) jobMember(process windows.Handle) (bool, error) {
	p.job.mu.Lock()
	defer p.job.mu.Unlock()
	if p.job.handle == 0 {
		return false, errors.New("unit job is closed")
	}
	return isProcessInJob(process, p.job.handle)
}

// drainNestedOutput keeps MAIN's stderr flowing and retains a bounded copy.
func drainNestedOutput(r io.Reader, path string) {
	f, err := os.Create(path)
	if err != nil {
		_, _ = io.Copy(io.Discard, r)
		return
	}
	defer f.Close()
	_, _ = io.Copy(f, io.LimitReader(r, 64<<10))
	_, _ = io.Copy(io.Discard, r)
}

// agentOwner drives an owner agent in a genuine S4U process.
type agentOwner struct {
	dir  string
	obs  *nestedjob.Observer
	proc UserManagerProc
	tok  *nestedjob.TokenContext
}

const agentTimeout = 30 * time.Second

// newAgentOwner launches the agent for the configured headless account, or
// skips when the lane is not configured. caseDir must already grant the
// account access.
func newAgentOwner(t *testing.T, sid, caseDir string) *agentOwner {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		t.Fatal("the headless owner lane needs a SYSTEM runner")
	}
	tok, err := ObtainLingerToken(LingerRecord{SID: sid})
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
	if tok.Info.SID != sid || tok.Source != LingerTokenPathS4U {
		t.Fatal("a genuine S4U token for the configured account is required")
	}
	broker, err := NativeOverlapBroker()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(caseDir, "owner")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	proc, err := StartUserManager(UserManagerSpec{
		SID: sid, Token: tok, Exe: testAbs(t), Daemon: broker, LoadProfile: true,
		ExtraArgs: []string{nestedOwnerSelector, "--agent-dir", dir},
	})
	if proc == nil && err != nil {
		t.Fatal(err)
	}
	a := &agentOwner{dir: dir, obs: nestedjob.NewObserver(caseDir), proc: proc}
	if err != nil {
		_ = proc.Kill()
		t.Fatal(err)
	}
	var self nestedjob.Identity
	deadline := time.Now().Add(agentTimeout)
	for {
		err := nestedjob.ReadJSON(filepath.Join(dir, "owner.json"), &self)
		if err == nil {
			break
		}
		if !proc.Alive() || time.Now().After(deadline) {
			_ = proc.Kill()
			t.Fatalf("owner agent did not start: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.tok = &nestedjob.TokenContext{SID: self.SID, Session: self.Session, Elevated: self.Elevated, Source: nestedjob.TokenS4U}
	if self.SID != sid || self.Session != 0 || self.Elevated || self.PID != uint32(proc.PID()) {
		_ = proc.Kill()
		t.Fatalf("owner agent token %+v is not the headless account in session zero", a.tok)
	}
	return a
}

func (a *agentOwner) command(t *testing.T, cmd nestedjob.Command) nestedjob.Ack {
	t.Helper()
	ack, err := a.obs.CommandIn(a.dir, nestedjob.RoleOwner, cmd, agentTimeout)
	if err != nil {
		t.Fatalf("owner agent %s: %v", cmd.Verb, err)
	}
	return ack
}

func (a *agentOwner) launch(t *testing.T, cfg nestedjob.MainConfig) nestedjob.Identity {
	t.Helper()
	ack := a.command(t, nestedjob.Command{Verb: nestedjob.VerbLaunch, Args: cfg.Args()})
	if ack.Identity == nil {
		t.Fatal("owner agent launched without an identity")
	}
	return *ack.Identity
}

func (a *agentOwner) inUnitJob(t *testing.T, h *nestedjob.Held) bool {
	t.Helper()
	ack := a.command(t, nestedjob.Command{Verb: nestedjob.VerbInUnitJob, PID: h.ID.PID, Created: h.ID.Created})
	if ack.InUnitJob == nil {
		t.Fatal("owner agent answered without membership")
	}
	return *ack.InUnitJob
}

func (a *agentOwner) pids(t *testing.T) []int {
	t.Helper()
	return a.command(t, nestedjob.Command{Verb: nestedjob.VerbPIDs}).PIDs
}

func (a *agentOwner) stop(timeout time.Duration) error {
	ms := timeout.Milliseconds()
	if ms > nestedjob.MaxOwnerStopMS {
		ms = nestedjob.MaxOwnerStopMS
	}
	_, err := a.obs.CommandIn(a.dir, nestedjob.RoleOwner, nestedjob.Command{Verb: nestedjob.VerbStop, TimeoutMS: ms}, timeout+agentTimeout)
	return err
}

func (a *agentOwner) token() *nestedjob.TokenContext { return a.tok }

// close asks the agent to drain and exit, then requires its process tree to
// have exited. Kill is the bounded fallback and still confirms exit.
func (a *agentOwner) close() error {
	_, exitErr := a.obs.CommandIn(a.dir, nestedjob.RoleOwner, nestedjob.Command{Verb: nestedjob.VerbExit}, agentTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), agentTimeout)
	defer cancel()
	waitErr := a.proc.Wait(ctx)
	if exitErr == nil && waitErr == nil && !a.proc.Alive() {
		return a.proc.Kill() // releases handles; the process has exited
	}
	return errors.Join(exitErr, waitErr, a.proc.Kill(), errors.New("owner agent did not exit after its exit command"))
}

// nestedOwnerAgentMain runs the agent and leaves any failure in its directory:
// a headless process has no console for standard error.
func nestedOwnerAgentMain(args []string) int {
	err := runNestedOwnerAgent(args)
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

// runNestedOwnerAgent is the agent process. It owns at most one unit, answers
// enumerated commands from the observer, drains the unit and exits on exit.
func runNestedOwnerAgent(args []string) error {
	dir := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--agent-dir" {
			dir = args[i+1]
		}
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return errors.New("owner agent needs --agent-dir")
	}
	self, err := nestedjob.SelfIdentity(nestedjob.RoleOwner, 0)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var proc *winProc
	exiting := false
	server := &nestedjob.CommandServer{Dir: dir, Role: nestedjob.RoleOwner, Handle: func(cmd nestedjob.Command) nestedjob.Ack {
		ack := nestedjob.Ack{Seq: cmd.Seq, Verb: cmd.Verb, OK: true}
		fail := func(err error) nestedjob.Ack {
			ack.OK = false
			ack.Failure = &nestedjob.Failure{Op: cmd.Verb, Message: err.Error()}
			return ack
		}
		switch cmd.Verb {
		case nestedjob.VerbLaunch:
			if proc != nil {
				return fail(errors.New("a unit is already launched"))
			}
			inv, err := nestedjob.Parse(cmd.Args)
			if err != nil {
				return fail(err)
			}
			argv := append([]string{exe, nestedjob.HelperSelector}, cmd.Args...)
			p, err := DefaultLauncher().Start(context.Background(), StartSpec{
				Unit: "nested.service", Type: unit.TypeSimple, Argv: argv, Dir: inv.Main.CaseDir,
			})
			if err != nil {
				return fail(err)
			}
			proc = p.(*winProc)
			go drainNestedOutput(p.Stderr(), filepath.Join(inv.Main.CaseDir, "main-stderr.txt"))
			created, err := nestedjob.CreationTime(proc.process)
			if err != nil {
				return fail(err)
			}
			ack.Identity = &nestedjob.Identity{Role: nestedjob.RoleMain, PID: uint32(p.PID()), Created: created}
		case nestedjob.VerbInUnitJob:
			if proc == nil {
				return fail(errors.New("no unit"))
			}
			h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, cmd.PID)
			if err != nil {
				return fail(err)
			}
			defer windows.CloseHandle(h)
			if created, err := nestedjob.CreationTime(h); err != nil || created != cmd.Created {
				return fail(fmt.Errorf("pid %d is not the observed process: %v", cmd.PID, err))
			}
			in, err := proc.jobMember(h)
			if err != nil {
				return fail(err)
			}
			ack.InUnitJob = &in
		case nestedjob.VerbPIDs:
			if proc == nil {
				return fail(errors.New("no unit"))
			}
			ids, err := proc.job.PIDs()
			if err != nil {
				return fail(err)
			}
			ack.PIDs = ids
		case nestedjob.VerbStop:
			if proc != nil {
				if err := proc.Stop(time.Duration(cmd.TimeoutMS) * time.Millisecond); err != nil {
					return fail(err)
				}
			}
		case nestedjob.VerbExit:
			if proc != nil {
				if err := proc.Stop(5 * time.Second); err != nil {
					return fail(err)
				}
			}
			exiting = true
		}
		return ack
	}}
	if err := nestedjob.WriteJSON(filepath.Join(dir, "owner.json"), self); err != nil {
		return err
	}
	// A lost observer must not leave the agent running: bound idle time.
	idle := time.Now().Add(10 * time.Minute)
	for !exiting {
		handled, err := server.Poll()
		if err != nil {
			return err
		}
		if handled {
			idle = time.Now().Add(10 * time.Minute)
		} else if time.Now().After(idle) {
			if proc != nil {
				_ = proc.Stop(5 * time.Second)
			}
			return errors.New("owner agent idle timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// nestedCaseDir returns a fresh case directory. With a headless SID it is
// protected for SYSTEM, Administrators and that account only. With the case
// root configured it is kept as evidence.
func nestedCaseDir(t *testing.T, sid string) string {
	t.Helper()
	root := os.Getenv(nestedjob.EnvCaseRoot)
	if root == "" {
		root = t.TempDir()
	} else if !filepath.IsAbs(root) {
		t.Fatal(nestedjob.EnvCaseRoot + " must be absolute")
	}
	dir, err := os.MkdirTemp(root, "case-")
	if err != nil {
		t.Fatal(err)
	}
	if sid != "" {
		if err := nestedjob.ProtectDirectory(dir, sid); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// newNestedOwner selects the owner for identity, or skips an unconfigured
// headless lane with the reason.
func newNestedOwner(t *testing.T, identity string) (nestedOwner, string, string) {
	t.Helper()
	switch identity {
	case nestedjob.IdentitySystem:
		return newLocalOwner(t), nestedCaseDir(t, ""), ""
	case nestedjob.IdentityHeadless:
		sid, err := nestedjob.HeadlessSID()
		if err != nil {
			t.Fatal(err)
		}
		if sid == "" {
			t.Skip("headless owner lane not configured: set " + nestedjob.EnvHeadlessSID + " to a dedicated logged-off local standard account and run as SYSTEM")
		}
		dir := nestedCaseDir(t, sid)
		return newAgentOwner(t, sid, dir), dir, sid
	}
	t.Fatalf("identity %q", identity)
	return nil, "", ""
}

// nestedRecordFor writes the scenario's result record after the test,
// including its cleanup, has finished.
func nestedRecordFor(t *testing.T, caseID, mode, identity, phase string) *nestedjob.Record {
	t.Helper()
	rec, err := nestedjob.NewRecord(caseID, mode, identity, phase)
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
