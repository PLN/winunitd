//go:build windows

package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"
	"unsafe"

	goruntime "runtime"

	"golang.org/x/sys/windows"
)

type userMgrProc struct {
	mu      sync.Mutex
	sid     string
	pid     int
	process windows.Handle
	job     *DaemonJob
	closed  bool
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
		_ = job.Close()
		return nil, err
	}
	if spec.Daemon != nil {
		_ = spec.Daemon.AssignPID(p.pid)
	}
	return p, nil
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

	if err := job.Assign(pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		return nil, err
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		return nil, fmt.Errorf("ResumeThread: %w", err)
	}
	_ = windows.CloseHandle(pi.Thread)

	return &userMgrProc{
		sid:     spec.SID,
		pid:     int(pi.ProcessId),
		process: pi.Process,
		job:     job,
	}, nil
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
	if p.job != nil {
		_ = p.job.Close()
	}
	if p.Alive() && p.pid > 0 {
		_ = terminatePIDHandle(p.pid)
	}
	_ = p.Wait(context.Background())
	return p.closeHandles()
}

func (p *userMgrProc) Wait(ctx context.Context) error {
	return waitPID(ctx, p.Alive, 5*time.Second)
}

func (p *userMgrProc) closeHandles() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.process != 0 {
		_ = windows.CloseHandle(p.process)
		p.process = 0
	}
	return nil
}
