//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
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

// StartUserManager launches winunitd --user-manager <SID> as the user
// via CreateProcessAsUser. spec.Token is a WTS token (interactive) or
// an S4U linger token. Missing token fails closed.
func StartUserManager(spec UserManagerSpec) (UserManagerProc, error) {
	if err := validateUserManagerSpec(spec); err != nil {
		return nil, err
	}
	tok, ok := nativeToken(spec.Token)
	if !ok {
		return nil, fmt.Errorf("%w: no WTS token handle for SID %s", ErrNoUserToken, spec.SID)
	}

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
	if spec.Daemon != nil {
		if err := assignDaemonPID(spec.Daemon, p.pid); err != nil {
			return failedUserManagerStart(p, err)
		}
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
	stdin, err := openNUL()
	if err != nil {
		return nil, fmt.Errorf("open NUL: %w", err)
	}
	stdout, err := openNUL()
	if err != nil {
		_ = windows.CloseHandle(stdin)
		return nil, fmt.Errorf("open NUL: %w", err)
	}
	stderr, err := openNUL()
	if err != nil {
		_ = windows.CloseHandle(stdin)
		_ = windows.CloseHandle(stdout)
		return nil, fmt.Errorf("open NUL: %w", err)
	}
	cleanup := func() {
		_ = windows.CloseHandle(stdin)
		_ = windows.CloseHandle(stdout)
		_ = windows.CloseHandle(stderr)
	}

	app, err := windows.UTF16PtrFromString(spec.Exe)
	if err != nil {
		cleanup()
		return nil, err
	}
	argv := spec.cmdArgv
	if len(argv) == 0 {
		argv = append([]string{spec.Exe}, UserManagerArgs(spec.SID, spec.ExtraArgs)...)
	}
	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		cleanup()
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
			cleanup()
			return nil, err
		}
	}
	block, err := envBlock(userManagerEnv(spec))
	if err != nil {
		cleanup()
		return nil, err
	}
	var envp *uint16
	if len(block) > 0 {
		envp = &block[0]
	}

	// Same inherit list as the unit launcher / Go StartProcess: inheritable
	// duplicates of stdin/stdout/stderr only. Blanket bInheritHandles=true
	// would leak the system manager's control-pipe listener, IOCPs, and
	// other units' pipes into a lower-privileged user process.
	dupIn, err := duplicateInheritable(stdin)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("duplicate stdin: %w", err)
	}
	dupOut, err := duplicateInheritable(stdout)
	if err != nil {
		_ = windows.CloseHandle(dupIn)
		cleanup()
		return nil, fmt.Errorf("duplicate stdout: %w", err)
	}
	dupErr, err := duplicateInheritable(stderr)
	if err != nil {
		_ = windows.CloseHandle(dupIn)
		_ = windows.CloseHandle(dupOut)
		cleanup()
		return nil, fmt.Errorf("duplicate stderr: %w", err)
	}
	closeDups := func() {
		_ = windows.CloseHandle(dupIn)
		_ = windows.CloseHandle(dupOut)
		_ = windows.CloseHandle(dupErr)
	}

	attrList, inherit, err := inheritHandleList(dupIn, dupOut, dupErr)
	if err != nil {
		closeDups()
		cleanup()
		return nil, err
	}
	defer attrList.Delete()

	// Heap StartupInfoEx so the attribute-list pointer stays valid across
	// the CreateProcessAsUser syscall (same as Go's StartProcess).
	si := &windows.StartupInfoEx{}
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput = dupIn
	si.StdOutput = dupOut
	si.StdErr = dupErr
	si.ProcThreadAttributeList = attrList.List()

	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW | windows.EXTENDED_STARTUPINFO_PRESENT)
	err = windows.CreateProcessAsUser(
		tok,
		app,
		cmdLine,
		nil,
		nil,
		len(inherit) > 0,
		flags,
		envp,
		dirp,
		&si.StartupInfo,
		&pi,
	)
	goruntime.KeepAlive(block)
	goruntime.KeepAlive(inherit)
	goruntime.KeepAlive(attrList)
	goruntime.KeepAlive(si)
	closeDups()
	cleanup()
	if err != nil {
		return nil, fmt.Errorf("CreateProcessAsUser %s: %w", spec.Exe, err)
	}

	p := &userMgrProc{
		sid: spec.SID, pid: int(pi.ProcessId), process: pi.Process,
		thread: pi.Thread, job: job, unassigned: true,
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
