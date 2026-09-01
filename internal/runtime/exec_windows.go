//go:build windows

package runtime

import (
	"context"
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

	if spec.Type == unit.TypeOneshot {
		waitCtx := ctx
		var cancel context.CancelFunc
		if spec.TimeoutStart > 0 {
			waitCtx, cancel = context.WithTimeout(ctx, spec.TimeoutStart)
			defer cancel()
		}
		if err := p.wait(waitCtx); err != nil {
			_ = p.Stop(0)
			if waitCtx.Err() != nil {
				return nil, fmt.Errorf("TimeoutStartSec exceeded: %w", waitCtx.Err())
			}
			return nil, err
		}
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
	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		cleanupHandles()
		return nil, fmt.Errorf("ProcThreadAttributeList: %w", err)
	}
	defer attrList.Delete()
	inherit := make([]windows.Handle, 0, 3)
	for _, h := range []windows.Handle{stdin, stdoutW, stderrW} {
		if h != 0 {
			inherit = append(inherit, h)
		}
	}
	if len(inherit) > 0 {
		if err := attrList.Update(
			windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
			unsafe.Pointer(&inherit[0]),
			uintptr(len(inherit))*unsafe.Sizeof(inherit[0]),
		); err != nil {
			cleanupHandles()
			return nil, fmt.Errorf("PROC_THREAD_ATTRIBUTE_HANDLE_LIST: %w", err)
		}
	}

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
	if l != nil && l.daemon != nil {
		// Nest under the daemon job when the child did not inherit it.
		// Already-in-job is expected after AssignSelf and is ignored.
		_ = l.daemon.AssignPID(int(pi.ProcessId))
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
	var sa windows.SecurityAttributes
	sa.Length = uint32(unsafe.Sizeof(sa))
	sa.InheritHandle = 1
	name, err := windows.UTF16PtrFromString(`NUL`)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		name,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		&sa,
		windows.OPEN_EXISTING,
		0,
		0,
	)
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
	defer windows.CloseHandle(dup)

	done := make(chan error, 1)
	go func() {
		s, err := windows.WaitForSingleObject(dup, windows.INFINITE)
		if err != nil {
			done <- err
			return
		}
		if s != windows.WAIT_OBJECT_0 {
			done <- fmt.Errorf("WaitForSingleObject: %d", s)
			return
		}
		var exit uint32
		if err := windows.GetExitCodeProcess(dup, &exit); err != nil {
			done <- err
			return
		}
		p.mu.Lock()
		p.exitCode = exit
		p.exited = true
		p.mu.Unlock()
		if exit == 0 {
			done <- nil
			return
		}
		done <- &ExitStatus{Code: exit}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}
