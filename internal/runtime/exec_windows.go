//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows"
)

const stillActiveExit = 259

type winLauncher struct {
	daemon *DaemonJob
}

func newLauncher(daemon *DaemonJob) Launcher {
	return &winLauncher{daemon: daemon}
}

type winProc struct {
	stopMu        sync.Mutex
	stopConfirmed bool // guarded by stopMu
	mu            sync.Mutex
	pid           int
	process       windows.Handle
	thread        windows.Handle
	unassigned    bool
	job           *UnitJob
	launchHandles [5]windows.Handle // stdin, stdout writer, stderr writer, stdout reader, stderr reader
	stdout        *ownedOutput
	stderr        *ownedOutput
	closed        bool
	exitCode      uint32
	exited        bool
}

func (l *winLauncher) Start(ctx context.Context, spec StartSpec) (Process, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		return nil, fmt.Errorf("ExecStart is empty")
	}

	if spec.Type == unit.TypeSimple && spec.TimeoutStart > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.TimeoutStart)
		defer cancel()
	}

	type outcome struct {
		p   *winProc
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		p, err := l.create(spec)
		ch <- outcome{p, err}
	}()

	select {
	case <-ctx.Done():
		o := <-ch
		return failedProcessStart(o.p, errors.Join(o.err, fmt.Errorf("TimeoutStartSec exceeded: %w", ctx.Err())))
	case o := <-ch:
		if err := ctx.Err(); err != nil {
			return failedProcessStart(o.p, errors.Join(o.err, err))
		}
		if o.err != nil {
			return failedProcessStart(o.p, o.err)
		}
		return o.p, nil
	}
}

// A failed launch may still own a process or handles. Transfer that ownership
// alongside the error when cleanup cannot finish; never hide it behind nil.
func failedProcessStart(p *winProc, cause error) (Process, error) {
	if p != nil {
		if err := p.Stop(2 * time.Second); err != nil {
			return p, errors.Join(cause, fmt.Errorf("launch cleanup: %w", err))
		}
	}
	return nil, cause
}

func (l *winLauncher) create(spec StartSpec) (*winProc, error) {
	return l.createWith(spec, OpenUnitJobWith, makeStdPipe, openNUL)
}

func (l *winLauncher) createWith(spec StartSpec, openJob func(JobLimits) (*UnitJob, error), openPipe func() (windows.Handle, windows.Handle, error), openInput func() (windows.Handle, error)) (*winProc, error) {
	return l.createWithSetup(spec, openJob, openPipe, openInput, processSetup{})
}

func (l *winLauncher) createWithSetup(spec StartSpec, openJob func(JobLimits) (*UnitJob, error), openPipe func() (windows.Handle, windows.Handle, error), openInput func() (windows.Handle, error), setup processSetup) (*winProc, error) {
	job, err := openJob(spec.Limits)
	p := &winProc{job: job}
	if err != nil {
		return p, err
	}
	stdoutR, stdoutW, err := openPipe()
	p.launchHandles[3], p.launchHandles[1] = stdoutR, stdoutW
	if err != nil {
		return p, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrR, stderrW, err := openPipe()
	p.launchHandles[4], p.launchHandles[2] = stderrR, stderrW
	if err != nil {
		return p, fmt.Errorf("stderr pipe: %w", err)
	}
	stdin, err := openInput()
	p.launchHandles[0] = stdin
	if err != nil {
		return p, fmt.Errorf("open NUL: %w", err)
	}

	app, err := windows.UTF16PtrFromString(spec.Argv[0])
	if err != nil {
		return p, err
	}
	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(spec.Argv))
	if err != nil {
		return p, err
	}
	var dirp *uint16
	if spec.Dir != "" {
		dirp, err = windows.UTF16PtrFromString(spec.Dir)
		if err != nil {
			return p, err
		}
	}
	block, err := envBlock(spec.Env)
	if err != nil {
		return p, err
	}
	var envp *uint16
	if len(block) > 0 {
		envp = &block[0]
	}

	// Only stdin/stdout/stderr are inherited. A blanket bInheritHandles=true
	// would leak the manager's listen sockets and IOCP into the unit process
	// (overlapped AcceptEx on those sockets can then fault the child).
	attrList, inherit, err := inheritHandleList(stdin, stdoutW, stderrW)
	if err != nil {
		return p, err
	}
	defer attrList.Delete()

	var si windows.StartupInfoEx
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput = stdin
	si.StdOutput = stdoutW
	si.StdErr = stderrW
	si.ProcThreadAttributeList = attrList.List()

	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW | windows.EXTENDED_STARTUPINFO_PRESENT)
	err = windows.CreateProcess(
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
	if err != nil {
		return p, fmt.Errorf("CreateProcess %s: %w", spec.Argv[0], err)
	}
	p.pid, p.process, p.thread = int(pi.ProcessId), pi.Process, pi.Thread
	p.unassigned = true
	p.stdout = newOwnedOutput(stdoutR, spec.Unit+"-stdout")
	p.stderr = newOwnedOutput(stderrR, spec.Unit+"-stderr")
	p.launchHandles[3], p.launchHandles[4] = 0, 0
	if err := p.closeLaunchHandles(); err != nil {
		return p, err
	}
	if err := l.finishProcessSetup(p, spec, setup); err != nil {
		return p, err
	}
	return p, nil
}

// Each launch has its own native setup operations. Tests can inject a failure at
// one boundary while retaining a real suspended process and real cleanup calls.
type processSetup struct {
	assignDaemon func(*DaemonJob, windows.Handle) error
	assignUnit   func(*UnitJob, windows.Handle) error
	priority     func(windows.Handle, uint32) error
	resume       func(windows.Handle) (uint32, error)
	closeThread  func(windows.Handle) error
}

func (l *winLauncher) finishProcessSetup(p *winProc, spec StartSpec, setup processSetup) error {
	if setup.assignDaemon == nil {
		setup.assignDaemon = assignDaemonProcess
	}
	if setup.assignUnit == nil {
		setup.assignUnit = (*UnitJob).Assign
	}
	if setup.priority == nil {
		setup.priority = setProcessIoPriority
	}
	if setup.resume == nil {
		setup.resume = windows.ResumeThread
	}
	if setup.closeThread == nil {
		setup.closeThread = windows.CloseHandle
	}
	// Attach the outer job first; each unit/user job must remain a sibling
	// under it, rather than making the daemon job a child of the first unit.
	if err := setup.assignDaemon(l.daemon, p.process); err != nil {
		return err
	}
	if err := setup.assignUnit(p.job, p.process); err != nil {
		return err
	}
	p.unassigned = false
	if spec.Limits.IoPrioritySet {
		if err := setup.priority(p.process, spec.Limits.IoPriority); err != nil {
			return err
		}
	}
	if _, err := setup.resume(p.thread); err != nil {
		return fmt.Errorf("ResumeThread: %w", err)
	}
	if err := setup.closeThread(p.thread); err != nil {
		return fmt.Errorf("close initial thread: %w", err)
	}
	p.thread = 0
	return nil
}

func makeStdPipe() (r, w windows.Handle, err error) {
	var sa windows.SecurityAttributes
	sa.Length = uint32(unsafe.Sizeof(sa))
	sa.InheritHandle = 1
	if err := windows.CreatePipe(&r, &w, &sa, 0); err != nil {
		return 0, 0, err
	}
	if err := windows.SetHandleInformation(r, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		// Transfer both allocations with the error; the launch owner retries
		// cleanup instead of losing a handle when CloseHandle also fails.
		return r, w, err
	}
	return r, w, nil
}

func openNUL() (windows.Handle, error) {
	// GENERIC_WRITE is required when this handle is used as stdout/stderr
	// (user-manager launch). GENERIC_READ covers stdin. NUL accepts both.
	return openNULAccess(windows.GENERIC_READ | windows.GENERIC_WRITE)
}

func openNULAccess(access uint32) (windows.Handle, error) {
	var sa windows.SecurityAttributes
	sa.Length = uint32(unsafe.Sizeof(sa))
	sa.InheritHandle = 1
	name, err := windows.UTF16PtrFromString(`NUL`)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		name,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		&sa,
		windows.OPEN_EXISTING,
		0,
		0,
	)
}

// inheritHandleList builds PROC_THREAD_ATTRIBUTE_HANDLE_LIST so only these
// handles are inherited. Callers must KeepAlive inherit until CreateProcess
// returns, then attrList.Delete(). Two attribute slots match Go's StartProcess
// (HANDLE_LIST plus optional PARENT_PROCESS).
func inheritHandleList(handles ...windows.Handle) (*windows.ProcThreadAttributeListContainer, []windows.Handle, error) {
	attrList, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, nil, fmt.Errorf("ProcThreadAttributeList: %w", err)
	}
	inherit := make([]windows.Handle, 0, len(handles))
	for _, h := range handles {
		if h == 0 || h == windows.InvalidHandle {
			continue
		}
		// HANDLE_LIST is ignored for non-inheritable handles; CreateFile
		// SECURITY_ATTRIBUTES is not always enough (see x/sys exec tests).
		if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			attrList.Delete()
			return nil, nil, fmt.Errorf("HANDLE_FLAG_INHERIT: %w", err)
		}
		inherit = append(inherit, h)
	}
	if len(inherit) > 0 {
		if err := attrList.Update(
			windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
			unsafe.Pointer(&inherit[0]),
			uintptr(len(inherit))*unsafe.Sizeof(inherit[0]),
		); err != nil {
			attrList.Delete()
			return nil, nil, fmt.Errorf("PROC_THREAD_ATTRIBUTE_HANDLE_LIST: %w", err)
		}
	}
	return attrList, inherit, nil
}

func envBlock(env []string) ([]uint16, error) {
	if env == nil {
		return nil, nil
	}
	sorted := append([]string(nil), env...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return strings.ToUpper(sorted[i]) < strings.ToUpper(sorted[j])
	})
	if len(sorted) == 0 {
		return []uint16{0, 0}, nil
	}
	var buf []uint16
	for _, e := range sorted {
		if strings.IndexByte(e, 0) >= 0 {
			return nil, fmt.Errorf("environment entry contains NUL")
		}
		u, err := windows.UTF16FromString(e)
		if err != nil {
			return nil, err
		}
		buf = append(buf, u...)
	}
	buf = append(buf, 0)
	return buf, nil
}

func (p *winProc) PID() int { return p.pid }

func (p *winProc) ExitCode() (uint32, bool) {
	if p == nil {
		return 0, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exited {
		return 0, false
	}
	return p.exitCode, true
}

func (p *winProc) Job() Job { return p.job }

func (p *winProc) Stdout() io.ReadCloser { return p.stdout }

func (p *winProc) Stderr() io.ReadCloser { return p.stderr }

func (p *winProc) Alive() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.process == 0 {
		return false
	}
	if p.exited {
		return false
	}
	var code uint32
	if err := windows.GetExitCodeProcess(p.process, &code); err != nil {
		return false
	}
	return code == stillActiveExit
}

func (p *winProc) Wait(ctx context.Context) error {
	return p.wait(ctx)
}

func (p *winProc) Stop(timeout time.Duration) error {
	if p == nil {
		return nil
	}
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil
	}
	if p.pid == 0 {
		// A failed bootstrap can own handles before any process exists.
		return p.closeHandles()
	}
	if !p.stopConfirmed {
		if p.unassigned && p.Alive() {
			if err := windows.TerminateProcess(p.process, 1); err != nil {
				return fmt.Errorf("terminate unassigned process: %w", err)
			}
		}
		if p.job != nil {
			if err := p.job.Kill(); err != nil {
				return err
			}
		}
		if timeout <= 0 {
			timeout = 2 * time.Second
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := p.wait(waitCtx); err != nil {
			var exited *ExitStatus
			if !errors.As(err, &exited) {
				return err
			}
		}
		if p.job != nil {
			if err := p.job.waitStopped(waitCtx); err != nil {
				return err
			}
		}
		p.stopConfirmed = true
	}
	return p.closeHandles()
}

func (p *winProc) Close() error {
	if p == nil {
		return nil
	}
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	return p.closeHandles()
}

// closeLaunchHandles is serialized by creation or stopMu. Attempt each
// independent release and clear only successful ones. Writers precede readers.
func (p *winProc) closeLaunchHandles() error {
	var errs []error
	for i, h := range p.launchHandles {
		if h == 0 {
			continue
		}
		if err := windows.CloseHandle(h); err != nil {
			errs = append(errs, fmt.Errorf("close launch handle %d: %w", i, err))
		} else {
			p.launchHandles[i] = 0
		}
	}
	return errors.Join(errs...)
}

func (p *winProc) closeHandles() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	job, h, thread, stdout, stderr := p.job, p.process, p.thread, p.stdout, p.stderr
	p.mu.Unlock()

	// Stop/Close serialize on stopMu. Publish each released handle only after
	// successful closure, leaving any unfinished release available for retry.
	if err := p.closeLaunchHandles(); err != nil {
		return err
	}
	if job != nil {
		if err := job.Close(); err != nil {
			return err
		}
	}
	if thread != 0 {
		if err := windows.CloseHandle(thread); err != nil {
			return fmt.Errorf("close initial thread: %w", err)
		}
		p.mu.Lock()
		p.thread = 0
		p.mu.Unlock()
	}
	if h != 0 {
		// Wait duplicates this handle under mu. Keep closure and publication
		// atomic so it cannot duplicate a closed/reused numeric handle.
		p.mu.Lock()
		err := windows.CloseHandle(h)
		if err == nil {
			p.process = 0
		}
		p.mu.Unlock()
		if err != nil {
			return fmt.Errorf("close process: %w", err)
		}
	}
	if stdout != nil {
		if err := stdout.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			return err
		}
		p.mu.Lock()
		p.stdout = nil
		p.mu.Unlock()
	}
	if stderr != nil {
		if err := stderr.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			return err
		}
		p.mu.Lock()
		p.stderr = nil
		p.mu.Unlock()
	}
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

// errWaitCanceled is sent when the wait goroutine wakes on the cancel
// event rather than process exit. Callers map it back to ctx.Err().
var errWaitCanceled = errors.New("process wait canceled")

func (p *winProc) wait(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	already := p.exited
	code := p.exitCode
	h := p.process
	if already {
		p.mu.Unlock()
		if code == 0 {
			return nil
		}
		return &ExitStatus{Code: code}
	}
	if h == 0 {
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
	return waitProcess(ctx, dup, func(exit uint32) {
		p.mu.Lock()
		p.exitCode = exit
		p.exited = true
		p.mu.Unlock()
	})
}
