//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unsafe"

	goruntime "runtime"

	"golang.org/x/sys/windows"
)

type userMgrProc struct {
	exits      jobExitSet
	killMu     sync.Mutex
	terminated bool // guarded by killMu; complete process-tree exit confirmed
	mu         sync.Mutex
	sid        string
	pid        int
	process    windows.Handle
	thread     windows.Handle
	unassigned bool
	job        *DaemonJob
	closed     bool
}

// PROC_THREAD_ATTRIBUTE_JOB_LIST from WinBase.h (Windows 10 / Server 2016).
// Not yet exported by the pinned x/sys/windows dependency.
const procThreadAttributeJobList = 0x0002000d

// StartUserManager launches winunitd --user-manager <SID> as the user
// via CreateProcessAsUser for interactive sessions or CreateProcessWithTokenW
// for Windows-owned headless profiles. Missing tokens fail closed.
func StartUserManager(spec UserManagerSpec) (UserManagerProc, error) {
	if err := validateUserManagerSpec(spec); err != nil {
		return nil, err
	}
	tok, ok := nativeToken(spec.Token)
	if !ok {
		return nil, fmt.Errorf("%w: no WTS token handle for SID %s", ErrNoUserToken, spec.SID)
	}
	var profile io.Closer
	if spec.LoadProfile {
		var session, returned uint32
		if err := windows.GetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), uint32(unsafe.Sizeof(session)), &returned); err != nil {
			return nil, fmt.Errorf("query profile launch session: %w", err)
		}
		if session == 0 {
			return startHeadlessUserManager(tok, spec)
		}
		var err error
		profile, err = loadUserManagerProfile(tok, spec.SID)
		if err != nil {
			return finishProfileLaunch(spec.SID, nil, profile, err)
		}
		info, err := userInfoFromToken(tok)
		if err != nil {
			return finishProfileLaunch(spec.SID, nil, profile, err)
		}
		copyToken := *spec.Token
		copyToken.Info = info
		spec.Token = &copyToken
	}
	proc, err := startUserManagerProcess(tok, spec)
	return finishProfileLaunch(spec.SID, proc, profile, err)
}

func startUserManagerProcess(tok windows.Token, spec UserManagerSpec) (UserManagerProc, error) {

	job, err := OpenDaemonJob()
	if err != nil {
		return nil, err
	}

	p, err := createUserManager(tok, spec, job)
	if err != nil {
		if p != nil {
			return failedUserManagerStart(p, err)
		}
		return nil, errors.Join(err, job.Close())
	}
	return p, nil
}

// failedUserManagerStart transfers ownership when cleanup cannot finish.
func failedUserManagerStart(p *userMgrProc, cause error) (UserManagerProc, error) {
	if err := p.Kill(); err != nil {
		return p, errors.Join(cause, fmt.Errorf("user manager launch cleanup: %w", err))
	}
	return nil, cause
}

func createUserManager(tok windows.Token, spec UserManagerSpec, job *DaemonJob) (*userMgrProc, error) {
	var session, returned uint32
	if err := windows.GetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), uint32(unsafe.Sizeof(session)), &returned); err != nil {
		return nil, fmt.Errorf("query user manager session: %w", err)
	}
	outer, boundaryFlags, err := userManagerJobBoundary(spec.Daemon, session)
	if err != nil {
		return nil, err
	}
	return createUserManagerWithBoundary(tok, spec, job, outer, boundaryFlags)
}

func createUserManagerWithBoundary(tok windows.Token, spec UserManagerSpec, job, outer *DaemonJob, boundaryFlags uint32) (*userMgrProc, error) {
	app, err := windows.UTF16PtrFromString(spec.Exe)
	if err != nil {
		return nil, err
	}
	argv := spec.cmdArgv
	if len(argv) == 0 {
		argv = append([]string{spec.Exe}, UserManagerArgs(spec.SID, spec.ExtraArgs)...)
	}
	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return nil, err
	}
	dir := ""
	if spec.Token != nil {
		dir = spec.Token.Info.Profile
	}
	var dirp *uint16
	if dir != "" {
		dirp, err = windows.UTF16PtrFromString(dir)
		if err != nil {
			return nil, err
		}
	}
	env, err := nativeUserManagerEnv(tok, spec)
	if err != nil {
		return nil, fmt.Errorf("user manager environment: %w", err)
	}
	block, err := envBlock(env)
	if err != nil {
		return nil, err
	}
	var envp *uint16
	if len(block) > 0 {
		envp = &block[0]
	}

	// Handle inheritance is prohibited across Windows sessions. The child
	// receives default standard streams from Windows; no broker handles cross
	// this boundary. Do not set STARTF_USESTDHANDLES or an inherited handle list.
	si := &windows.StartupInfoEx{}
	si.Cb = uint32(unsafe.Sizeof(*si))
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	flags |= boundaryFlags
	if boundaryFlags&windows.CREATE_BREAKAWAY_FROM_JOB != 0 {
		// Atomic placement prevents a broker crash between process creation and
		// assignment from abandoning a suspended process outside all owned jobs.
		attrs, err := windows.NewProcThreadAttributeList(1)
		if err != nil {
			return nil, fmt.Errorf("create user job attributes: %w", err)
		}
		defer attrs.Delete()
		if err := attrs.Update(procThreadAttributeJobList, unsafe.Pointer(&job.handle), unsafe.Sizeof(job.handle)); err != nil {
			return nil, fmt.Errorf("set user job attribute: %w", err)
		}
		si.ProcThreadAttributeList = attrs.List()
		flags |= windows.EXTENDED_STARTUPINFO_PRESENT
	} else {
		si.Cb = uint32(unsafe.Sizeof(si.StartupInfo))
	}
	err = windows.CreateProcessAsUser(
		tok,
		app,
		cmdLine,
		nil,
		nil,
		false,
		flags,
		envp,
		dirp,
		&si.StartupInfo,
		&pi,
	)
	goruntime.KeepAlive(block)
	goruntime.KeepAlive(si)
	if err != nil {
		return nil, fmt.Errorf("CreateProcessAsUser %s: %w", spec.Exe, err)
	}

	p := &userMgrProc{
		sid: spec.SID, pid: int(pi.ProcessId), process: pi.Process,
		thread: pi.Thread, job: job, unassigned: true,
	}
	// Same-session managers nest under the outer job. Cross-session managers
	// cannot join that job: their dedicated job remains owned by this broker,
	// with no inherited handles, and is attached before the child runs.
	if err := assignDaemonProcess(outer, pi.Process); err != nil {
		return p, err
	}
	if err := job.Assign(pi.Process); err != nil {
		return p, err
	}
	p.unassigned = false
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		return p, fmt.Errorf("ResumeThread: %w", err)
	}
	if err := windows.CloseHandle(pi.Thread); err != nil {
		return p, fmt.Errorf("close initial user manager thread: %w", err)
	}
	p.thread = 0
	return p, nil
}

func userManagerJobBoundary(daemon *DaemonJob, targetSession uint32) (*DaemonJob, uint32, error) {
	var callerSession uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &callerSession); err != nil {
		return nil, 0, fmt.Errorf("query broker session: %w", err)
	}
	if targetSession == callerSession || daemon == nil {
		return daemon, 0, nil
	}
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	if daemon.handle == 0 || !daemon.broker {
		return nil, 0, fmt.Errorf("cross-session user manager requires an open broker job")
	}
	member, err := isProcessInJob(windows.CurrentProcess(), daemon.handle)
	if err != nil {
		return nil, 0, fmt.Errorf("query broker job membership: %w", err)
	}
	if !member {
		return nil, 0, fmt.Errorf("cross-session user manager requires broker self-assignment")
	}
	return nil, windows.CREATE_BREAKAWAY_FROM_JOB, nil
}

func nativeUserManagerEnv(tok windows.Token, spec UserManagerSpec) ([]string, error) {
	if spec.Env != nil {
		return spec.Env, nil // explicit caller environment (native test fixtures)
	}
	identity, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	if identity.User.Sid.String() != spec.SID {
		return nil, fmt.Errorf("target token SID mismatch")
	}
	env, err := tok.Environ(false)
	if err != nil {
		return nil, err
	}
	// CreateEnvironmentBlock can succeed with only machine variables when the
	// user profile is unavailable. Do not disguise that as a user environment
	// by synthesizing USERPROFILE afterwards. Profile loading remains explicit.
	hasProfile := false
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, "USERPROFILE") && value != "" {
			hasProfile = true
			break
		}
	}
	if !hasProfile {
		return nil, fmt.Errorf("target user profile environment is unavailable")
	}
	return MergeDeterministicUserEnv(env, spec.Token.Info), nil
}

func (p *userMgrProc) PID() int    { return p.pid }
func (p *userMgrProc) SID() string { return p.sid }

func (p *userMgrProc) Alive() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.process == 0 {
		return false
	}
	var code uint32
	if err := windows.GetExitCodeProcess(p.process, &code); err != nil {
		return false
	}
	return code == stillActiveExit
}

func (p *userMgrProc) Kill() error {
	if p == nil {
		return nil
	}
	p.killMu.Lock()
	defer p.killMu.Unlock()
	p.mu.Lock()
	closed, process := p.closed, p.process
	p.mu.Unlock()
	if closed {
		return nil
	}
	// This is the dedicated child-manager job, never the system daemon's job.
	// Retain its handle until termination and all descendants are confirmed.
	if p.job != nil {
		p.job.mu.Lock()
		defer p.job.mu.Unlock()
	}
	if !p.terminated {
		// A failed assignment leaves the suspended child outside the job.
		// Terminate only the process handle obtained at creation.
		if p.unassigned && p.Alive() {
			if err := windows.TerminateProcess(process, 1); err != nil {
				return fmt.Errorf("terminate unassigned user manager: %w", err)
			}
		}
		if p.job != nil {
			if p.job.handle == 0 {
				return fmt.Errorf("user manager job is closed before exit confirmation")
			}
			if err := p.exits.prepare(p.job.handle); err != nil {
				return err
			}
			if err := windows.TerminateJobObject(p.job.handle, 1); err != nil {
				return fmt.Errorf("terminate user manager job: %w", err)
			}
		} else if p.Alive() {
			if err := windows.TerminateProcess(process, 1); err != nil {
				return fmt.Errorf("terminate user manager: %w", err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Wait(ctx); err != nil {
			var exited *ExitStatus
			if !errors.As(err, &exited) {
				return err
			}
		}
		if p.job != nil {
			if err := p.exits.wait(ctx); err != nil {
				return err
			}
			if err := waitProcessListEmpty(ctx, func() ([]int, error) { return queryJobPIDs(p.job.handle) }); err != nil {
				return err
			}
		}
		p.terminated = true
	}
	if err := p.exits.close(); err != nil {
		return err
	}
	if p.job != nil && p.job.handle != 0 {
		if err := windows.CloseHandle(p.job.handle); err != nil {
			return fmt.Errorf("close user manager job: %w", err)
		}
		p.job.handle = 0
	}
	return p.closeHandles()
}

func (p *userMgrProc) Wait(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	h := p.process
	if p.closed || h == 0 {
		p.mu.Unlock()
		return fmt.Errorf("process handle is closed")
	}
	var dup windows.Handle
	err := windows.DuplicateHandle(
		windows.CurrentProcess(),
		h,
		windows.CurrentProcess(),
		&dup,
		0,
		false,
		windows.DUPLICATE_SAME_ACCESS,
	)
	p.mu.Unlock()
	if err != nil {
		return err
	}
	return waitProcess(ctx, dup, nil)
}

func (p *userMgrProc) closeHandles() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if p.thread != 0 {
		if err := windows.CloseHandle(p.thread); err != nil {
			return fmt.Errorf("close initial user manager thread: %w", err)
		}
		p.thread = 0
	}
	if p.process != 0 {
		if err := windows.CloseHandle(p.process); err != nil {
			return fmt.Errorf("close user manager process: %w", err)
		}
		p.process = 0
	}
	p.closed = true
	return nil
}
