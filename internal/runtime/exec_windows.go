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
	mu       sync.Mutex
	pid      int
	process  windows.Handle
	job      *UnitJob
	stdout   *os.File
	stderr   *os.File
	closed   bool
	exitCode uint32
	exited   bool
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

	var p *winProc
	select {
	case <-ctx.Done():
		o := <-ch
		if o.p != nil {
			_ = o.p.Stop(0)
		}
		if o.err != nil {
			return nil, o.err
		}
		return nil, fmt.Errorf("TimeoutStartSec exceeded: %w", ctx.Err())
	case o := <-ch:
		if o.err != nil {
			return nil, o.err
		}
		p = o.p
	}

	return p, nil
}

func (l *winLauncher) create(spec StartSpec) (*winProc, error) {
	job, err := OpenUnitJobWith(spec.Limits)
	if err != nil {
		return nil, err
	}

	stdoutR, stdoutW, err := makeStdPipe()
	if err != nil {
		_ = job.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrR, stderrW, err := makeStdPipe()
	if err != nil {
		_ = windows.CloseHandle(stdoutR)
		_ = windows.CloseHandle(stdoutW)
		_ = job.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	stdin, err := openNUL()
	if err != nil {
		_ = windows.CloseHandle(stdoutR)
		_ = windows.CloseHandle(stdoutW)
		_ = windows.CloseHandle(stderrR)
		_ = windows.CloseHandle(stderrW)
		_ = job.Close()
		return nil, fmt.Errorf("open NUL: %w", err)
	}

	cleanupHandles := func() {
		_ = windows.CloseHandle(stdoutR)
		_ = windows.CloseHandle(stdoutW)
		_ = windows.CloseHandle(stderrR)
		_ = windows.CloseHandle(stderrW)
		_ = windows.CloseHandle(stdin)
		_ = job.Close()
	}

	app, err := windows.UTF16PtrFromString(spec.Argv[0])
	if err != nil {
		cleanupHandles()
		return nil, err
	}
	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(spec.Argv))
	if err != nil {
		cleanupHandles()
		return nil, err
	}
	var dirp *uint16
	if spec.Dir != "" {
		dirp, err = windows.UTF16PtrFromString(spec.Dir)
		if err != nil {
			cleanupHandles()
			return nil, err
		}
	}
	block, err := envBlock(spec.Env)
	if err != nil {
		cleanupHandles()
		return nil, err
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
		cleanupHandles()
		return nil, err
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
	_ = windows.CloseHandle(stdin)
	_ = windows.CloseHandle(stdoutW)
	_ = windows.CloseHandle(stderrW)
	stdin = 0
	stdoutW = 0
	stderrW = 0
	if err != nil {
		_ = windows.CloseHandle(stdoutR)
		_ = windows.CloseHandle(stderrR)
		_ = job.Close()
		return nil, fmt.Errorf("CreateProcess %s: %w", spec.Argv[0], err)
	}

	if err := job.Assign(pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(stdoutR)
		_ = windows.CloseHandle(stderrR)
		_ = job.Close()
		return nil, err
	}
	if spec.Limits.IoPrioritySet {
		if err := setProcessIoPriority(pi.Process, spec.Limits.IoPriority); err != nil {
			_ = windows.TerminateProcess(pi.Process, 1)
			_ = windows.CloseHandle(pi.Thread)
			_ = windows.CloseHandle(pi.Process)
			_ = windows.CloseHandle(stdoutR)
			_ = windows.CloseHandle(stderrR)
			_ = job.Close()
			return nil, err
		}
	}
	if err := assignDaemonPID(l.daemon, int(pi.ProcessId)); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(stdoutR)
		_ = windows.CloseHandle(stderrR)
		_ = job.Close()
		return nil, err
	}

	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		_ = windows.CloseHandle(stdoutR)
		_ = windows.CloseHandle(stderrR)
		_ = job.Close()
		return nil, fmt.Errorf("ResumeThread: %w", err)
	}
	_ = windows.CloseHandle(pi.Thread)

	return &winProc{
		pid:     int(pi.ProcessId),
		process: pi.Process,
		job:     job,
		stdout:  os.NewFile(uintptr(stdoutR), spec.Unit+"-stdout"),
		stderr:  os.NewFile(uintptr(stderrR), spec.Unit+"-stderr"),
	}, nil
}

func makeStdPipe() (r, w windows.Handle, err error) {
	var sa windows.SecurityAttributes
	sa.Length = uint32(unsafe.Sizeof(sa))
	sa.InheritHandle = 1
	if err := windows.CreatePipe(&r, &w, &sa, 0); err != nil {
		return 0, 0, err
	}
	if err := windows.SetHandleInformation(r, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		_ = windows.CloseHandle(r)
		_ = windows.CloseHandle(w)
		return 0, 0, err
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

// duplicateInheritable copies h into this process with HANDLE_FLAG_INHERIT set.
// Go's StartProcess does this before PROC_THREAD_ATTRIBUTE_HANDLE_LIST so the
// listed handles are independently inheritable copies, not the caller's originals.
func duplicateInheritable(h windows.Handle) (windows.Handle, error) {
	if h == 0 || h == windows.InvalidHandle {
		return 0, nil
	}
	var dup windows.Handle
	err := windows.DuplicateHandle(
		windows.CurrentProcess(),
		h,
		windows.CurrentProcess(),
		&dup,
		0,
		true,
		windows.DUPLICATE_SAME_ACCESS,
	)
	if err != nil {
		return 0, err
	}
	return dup, nil
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
	if p.job != nil {
		_ = p.job.Kill()
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = p.wait(waitCtx)
	return p.Close()
}

func (p *winProc) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	job := p.job
	h := p.process
	p.process = 0
	stdout := p.stdout
	stderr := p.stderr
	p.stdout = nil
	p.stderr = nil
	p.mu.Unlock()

	// Do not hold p.mu while closing the job or pipes. watch's Wait
	// records exit under p.mu, and journal capture reads these pipes.
	// C2 teardown (launchUnit eviction) runs Close concurrently with Wait.
	if job != nil {
		_ = job.Close()
	}
	if h != 0 {
		_ = windows.CloseHandle(h)
	}
	if stdout != nil {
		_ = stdout.Close()
	}
	if stderr != nil {
		_ = stderr.Close()
	}
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
