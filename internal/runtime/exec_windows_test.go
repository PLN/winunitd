//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows"
)

func testAbs(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func startHelper(t *testing.T, mode string, typ unit.ServiceType, timeout time.Duration) Process {
	t.Helper()
	return startHelperLimits(t, mode, typ, timeout, JobLimits{})
}

func startHelperLimits(t *testing.T, mode string, typ unit.ServiceType, timeout time.Duration, lim JobLimits) Process {
	t.Helper()
	env := helperEnv("WINUNITD_JOB_HELPER=" + mode)
	p, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Unit:         "test.service",
		Type:         typ,
		Argv:         []string{testAbs(t), winunitdHelperArgPrefix + mode},
		Dir:          t.TempDir(),
		Env:          env,
		TimeoutStart: timeout,
		Limits:       lim,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(2 * time.Second) })
	return p
}

func readLine(t *testing.T, r io.Reader, timeout time.Duration) string {
	t.Helper()
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var buf []byte
		tmp := make([]byte, 1)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				if tmp[0] == '\n' {
					ch <- result{s: strings.TrimRight(string(buf), "\r")}
					return
				}
				buf = append(buf, tmp[0])
			}
			if err != nil {
				if len(buf) > 0 && err == io.EOF {
					ch <- result{s: strings.TrimRight(string(buf), "\r")}
					return
				}
				ch <- result{err: err}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.s
	case <-time.After(timeout):
		t.Fatal("timeout reading helper output")
		return ""
	}
}

func parseChildPID(t *testing.T, line string) int {
	t.Helper()
	var pid int
	if _, err := fmt.Sscanf(line, "child %d", &pid); err != nil {
		n, convErr := strconv.Atoi(strings.TrimPrefix(line, "child "))
		if convErr != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		pid = n
	}
	if pid <= 0 {
		t.Fatalf("bad child line %q", line)
	}
	return pid
}

func TestUnitJobNoBreakawayFlags(t *testing.T) {
	job, err := OpenUnitJob()
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	flags, err := job.LimitFlags()
	if err != nil {
		t.Fatal(err)
	}
	if flags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		t.Fatal("KILL_ON_JOB_CLOSE not set")
	}
	if flags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0 {
		t.Fatal("BREAKAWAY_OK must not be set")
	}
	if flags&windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK != 0 {
		t.Fatal("SILENT_BREAKAWAY_OK must not be set")
	}
	got, err := job.QueryLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY != 0 {
		t.Fatal("no MemoryMax: JOB_OBJECT_LIMIT_JOB_MEMORY must not be set")
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS != 0 {
		t.Fatal("no ProcessLimit: JOB_OBJECT_LIMIT_ACTIVE_PROCESS must not be set")
	}
	if got.LimitFlags&windows.JOB_OBJECT_LIMIT_PRIORITY_CLASS != 0 {
		t.Fatal("no PriorityClass: JOB_OBJECT_LIMIT_PRIORITY_CLASS must not be set")
	}
	if got.CPUControlFlags&JobCPURateEnable != 0 {
		t.Fatal("no CPUWeight/CPUQuota: CPU rate control must not be set")
	}
}

func TestStartedUnitIsInOwnJob(t *testing.T) {
	p := startHelper(t, "sleep", unit.TypeSimple, 0)
	if p.PID() <= 0 {
		t.Fatalf("pid = %d", p.PID())
	}
	in, err := p.Job().Contains(p.PID())
	if err != nil {
		t.Fatal(err)
	}
	if !in {
		t.Fatal("started unit is not in its unit job")
	}
	if !p.Alive() {
		t.Fatal("simple unit died during CreateProcess")
	}
}

func TestSimpleStartDoesNotWaitForReady(t *testing.T) {
	start := time.Now()
	p := startHelper(t, "sleep", unit.TypeSimple, 5*time.Second)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Type=simple Start took %s; TimeoutStartSec must only bound CreateProcess", elapsed)
	}
	if !p.Alive() {
		t.Fatal("sleeper not running")
	}
}

func TestOneshotWaitsForExit(t *testing.T) {
	start := time.Now()
	p, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Unit: "oneshot.service",
		Type: unit.TypeOneshot,
		Argv: []string{testAbs(t), winunitdHelperArgPrefix + "oneshot"},
		Dir:  t.TempDir(),
		Env:  helperEnv("WINUNITD_JOB_HELPER=oneshot"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("oneshot hung")
	}
	if p.Alive() {
		t.Fatal("oneshot still running after Start")
	}
}

func TestOneshotReturnsBeforeCompletion(t *testing.T) {
	p, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Unit:         "oneshot.service",
		Type:         unit.TypeOneshot,
		Argv:         []string{testAbs(t), winunitdHelperArgPrefix + "sleep"},
		Dir:          t.TempDir(),
		Env:          helperEnv("WINUNITD_JOB_HELPER=sleep"),
		TimeoutStart: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(time.Second)
	if !p.Alive() {
		t.Fatal("launcher waited for oneshot completion")
	}
}

func TestStopWaitFailurePreservesProcessHandle(t *testing.T) {
	p := startHelper(t, "sleep", unit.TypeSimple, 0).(*winProc)
	job := p.job
	// A no-op job cannot terminate the process; the wait must time out.
	p.job = &UnitJob{}
	t.Cleanup(func() { p.job = job; _ = p.Stop(time.Second) })
	if err := p.Stop(20 * time.Millisecond); err == nil {
		t.Fatal("unconfirmed process exit reported success")
	}
	if !p.Alive() {
		t.Fatal("failed stop closed the process handle")
	}
	p.job = job
	if err := p.Stop(time.Second); err != nil {
		t.Fatal("retry failed", err)
	}
	if p.Alive() {
		t.Fatal("retry left process alive")
	}
}

func TestStopJobQueryFailurePreservesProcessHandle(t *testing.T) {
	p := startHelper(t, "sleep", unit.TypeSimple, 0).(*winProc)
	job := p.job
	if err := job.Kill(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.Wait(ctx)
	if p.Alive() {
		t.Fatal("main process did not exit")
	}
	// This job's Kill is a no-op, but querying its process list fails. Main
	// process exit alone must not allow Stop to discard the remaining handles.
	p.job = &UnitJob{}
	t.Cleanup(func() { p.job = job; _ = p.Stop(time.Second) })
	if err := p.Stop(time.Second); err == nil {
		t.Fatal("failed job query reported confirmed termination")
	}
	p.mu.Lock()
	retained := !p.closed && p.process != 0
	p.mu.Unlock()
	if !retained {
		t.Fatal("failed job query discarded process handle")
	}
	p.job = job
	if err := p.Stop(time.Second); err != nil {
		t.Fatal("retry failed", err)
	}
}

func TestStopKillFailurePreservesProcessHandle(t *testing.T) {
	p := startHelper(t, "sleep", unit.TypeSimple, 0).(*winProc)
	job := p.job
	p.job = &UnitJob{handle: windows.InvalidHandle}
	t.Cleanup(func() { p.job = job; _ = p.Stop(time.Second) })
	if err := p.Stop(time.Second); err == nil {
		t.Fatal("job termination error was ignored")
	}
	if !p.Alive() {
		t.Fatal("failed kill discarded the live process handle")
	}
	p.job = job
	if err := p.Stop(time.Second); err != nil {
		t.Fatal("retry failed", err)
	}
}

func TestStdoutStderrAttached(t *testing.T) {
	p := startHelper(t, "hello", unit.TypeSimple, 0)
	line := readLine(t, p.Stdout(), 5*time.Second)
	if line != "hello-stdout" {
		t.Fatalf("stdout = %q", line)
	}
	errLine := readLine(t, p.Stderr(), 5*time.Second)
	if errLine != "hello-stderr" {
		t.Fatalf("stderr = %q", errLine)
	}
}

func TestGrandchildCannotBreakAway(t *testing.T) {
	p := startHelper(t, "breakaway", unit.TypeSimple, 0)
	line1 := readLine(t, p.Stdout(), 5*time.Second)
	if line1 != "breakaway-denied" {
		t.Fatalf("breakaway result = %q (want breakaway-denied)", line1)
	}
	line2 := readLine(t, p.Stdout(), 5*time.Second)
	child := parseChildPID(t, line2)
	in, err := p.Job().Contains(child)
	if err != nil {
		t.Fatal(err)
	}
	if !in {
		t.Fatalf("grandchild pid %d is not in the unit job", child)
	}
}

func TestKillUnitJobTearsDownTree(t *testing.T) {
	p := startHelper(t, "spawn", unit.TypeSimple, 0)
	line := readLine(t, p.Stdout(), 5*time.Second)
	child := parseChildPID(t, line)
	if !processAlive(p.PID()) {
		t.Fatal("parent died before job close")
	}
	if !processAlive(child) {
		t.Fatal("grandchild died before job close")
	}
	in, err := p.Job().Contains(child)
	if err != nil {
		t.Fatal(err)
	}
	if !in {
		t.Fatalf("grandchild pid %d is not in the unit job", child)
	}
	if err := p.Job().Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(p.PID()) && !processAlive(child) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("tree still alive parent=%v child=%v", processAlive(p.PID()), processAlive(child))
}

func TestOneshotFailureReturnsExitStatus(t *testing.T) {
	p, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Unit: "fail.service",
		Type: unit.TypeOneshot,
		Argv: []string{testAbs(t), winunitdHelperArgPrefix + "fail"},
		Dir:  t.TempDir(),
		Env:  helperEnv("WINUNITD_JOB_HELPER=fail"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = p.Wait(ctx)
	var st *ExitStatus
	if !errors.As(err, &st) || st.Code != 2 {
		t.Fatalf("err = %v", err)
	}
}

func TestStopKillsUnitJobTree(t *testing.T) {
	p := startHelper(t, "spawn", unit.TypeSimple, 0)
	line := readLine(t, p.Stdout(), 5*time.Second)
	child := parseChildPID(t, line)
	childHandle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(child))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(childHandle)
	if err := p.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	state, err := windows.WaitForSingleObject(childHandle, 0)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("child not exited when Stop returned: wait=%d err=%v", state, err)
	}
}

func TestKilledJobEmptiesBeforeProcessHandleClose(t *testing.T) {
	p := startHelper(t, "spawn", unit.TypeSimple, 0)
	_ = parseChildPID(t, readLine(t, p.Stdout(), 5*time.Second))
	if err := p.Job().Kill(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = p.Wait(ctx)
	for {
		ids, err := p.Job().PIDs()
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("job retains %d process IDs with exited main handle open", len(ids))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestCreateProcessWithLoopbackListenerKeepsHelperAlive(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	ping := filepath.Join(root, "System32", "ping.exe")
	if _, err := os.Stat(ping); err != nil {
		t.Fatalf("ping.exe: %v", err)
	}
	p, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Unit: "listener.service",
		Type: unit.TypeSimple,
		Argv: []string{ping, "-t", "127.0.0.1"},
		Dir:  t.TempDir(),
		Env:  helperEnv(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(2 * time.Second) })
	time.Sleep(400 * time.Millisecond)
	if !p.Alive() {
		t.Fatal("ping helper died while parent held a loopback listener")
	}
}

func TestWaitCancelDoesNotCloseWaitedHandle(t *testing.T) {
	// ping -t stays running without TestMain. A Go test-binary helper can
	// exit 2 (runtime fatal / unknown flag) and that is not a Wait-cancel bug.
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	ping := filepath.Join(root, "System32", "ping.exe")
	if _, err := os.Stat(ping); err != nil {
		t.Fatalf("ping.exe: %v", err)
	}
	p, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Unit: "waitcancel.service",
		Type: unit.TypeSimple,
		Argv: []string{ping, "-t", "127.0.0.1"},
		Dir:  t.TempDir(),
		Env:  helperEnv(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(2 * time.Second) })
	if !p.Alive() {
		t.Fatal("ping helper died before Wait")
	}

	goruntime.GC()
	baseline := goruntime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err = p.Wait(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want context.DeadlineExceeded", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		n = goruntime.NumGoroutine()
		if n <= baseline+1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n > baseline+2 {
		t.Fatalf("goroutines after cancel: %d (baseline %d)", n, baseline)
	}

	if _, exited := p.ExitCode(); exited {
		t.Fatal("ExitCode recorded after cancelled Wait")
	}
	wp := p.(*winProc)
	wp.mu.Lock()
	exited := wp.exited
	wp.mu.Unlock()
	if exited {
		t.Fatal("p.exited is true after cancelled Wait")
	}
	if !p.Alive() {
		t.Fatal("helper exited during cancelled Wait")
	}

	if err := p.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	code, ok := p.ExitCode()
	if !ok {
		t.Fatal("Stop did not record an exit code")
	}
	if code == stillActiveExit {
		t.Fatalf("exit code still STILL_ACTIVE (%d)", code)
	}
}

func TestStopClosesJobAdmissionBeforeExitCapture(t *testing.T) {
	p := startHelper(t, "sleep", unit.TypeSimple, 0).(*winProc)
	j := p.job
	j.stopMu.Lock()
	j.mu.Lock()
	err := j.exits.prepare(j.handle)
	j.mu.Unlock()
	j.stopMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !p.Alive() {
		t.Fatal("closing admission terminated the existing process")
	}
	other := startHelper(t, "sleep", unit.TypeSimple, 0).(*winProc)
	if err := j.Assign(other.process); err == nil {
		t.Fatal("job admitted another process after exit snapshot")
	}
	if err := p.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestCapturedProcessExitDeadlineRetainsHandle(t *testing.T) {
	p := startHelper(t, "sleep", unit.TypeSimple, 0).(*winProc)
	var dup windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), p.process, windows.CurrentProcess(), &dup, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatal(err)
	}
	s := jobExitSet{ready: true, handles: map[int]windows.Handle{p.pid: dup}}
	defer s.close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pending process exit wait = %v", err)
	}
	state, err := windows.WaitForSingleObject(dup, 0)
	if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("deadline closed or signaled a live process handle")
	}
	if err := p.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.wait(context.Background()); err != nil {
		t.Fatal("exit confirmation retry", err)
	}
}

func TestUnitJobCloseFailureRetainsHandle(t *testing.T) {
	const handleFlagProtectFromClose = 0x00000002
	j, err := OpenUnitJob()
	if err != nil {
		t.Fatal(err)
	}
	h := j.handle
	t.Cleanup(func() {
		if j.handle == h {
			_ = windows.SetHandleInformation(h, handleFlagProtectFromClose, 0)
			_ = j.Close()
		}
	})
	if err := windows.SetHandleInformation(h, handleFlagProtectFromClose, handleFlagProtectFromClose); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err == nil {
		t.Fatal("protected job handle close reported success")
	}
	if j.handle != h {
		t.Fatal("failed close discarded the handle")
	}
	if err := windows.SetHandleInformation(h, handleFlagProtectFromClose, 0); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal("close retry failed", err)
	}
}

func TestStopRetriesProtectedProcessHandleClose(t *testing.T) {
	const handleFlagProtectFromClose = 0x00000002
	p := startHelper(t, "sleep", unit.TypeSimple, 0).(*winProc)
	h := p.process
	t.Cleanup(func() {
		p.mu.Lock()
		owned := p.process == h
		p.mu.Unlock()
		if owned {
			_ = windows.SetHandleInformation(h, handleFlagProtectFromClose, 0)
			_ = p.Stop(time.Second)
		}
	})
	if err := windows.SetHandleInformation(h, handleFlagProtectFromClose, handleFlagProtectFromClose); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(time.Second); err == nil {
		t.Fatal("protected process handle close reported success")
	}
	p.mu.Lock()
	retained := !p.closed && p.process == h
	p.mu.Unlock()
	if !retained {
		t.Fatal("failed close lost process handle")
	}
	if err := windows.SetHandleInformation(h, handleFlagProtectFromClose, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(time.Second); err != nil {
		t.Fatal("stop retry after confirmed exit failed", err)
	}
}

func TestFailedStartTransfersUnfinishedCleanup(t *testing.T) {
	const handleFlagProtectFromClose = 0x00000002
	p := startHelper(t, "sleep", unit.TypeSimple, 0).(*winProc)
	h := p.process
	t.Cleanup(func() {
		p.mu.Lock()
		owned := p.process == h
		p.mu.Unlock()
		if owned {
			_ = windows.SetHandleInformation(h, handleFlagProtectFromClose, 0)
			_ = p.Stop(time.Second)
		}
	})
	if err := windows.SetHandleInformation(h, handleFlagProtectFromClose, handleFlagProtectFromClose); err != nil {
		t.Fatal(err)
	}
	owned, err := failedProcessStart(p, context.Canceled)
	if owned != p || !errors.Is(err, context.Canceled) {
		t.Fatal("failed start lost cleanup ownership or original error")
	}
	if err := windows.SetHandleInformation(h, handleFlagProtectFromClose, 0); err != nil {
		t.Fatal(err)
	}
	if err := owned.Stop(time.Second); err != nil {
		t.Fatal("cleanup retry", err)
	}
}

func TestStopCleansProcessNotAssignedToUnitJob(t *testing.T) {
	p := startHelper(t, "sleep", unit.TypeSimple, 0).(*winProc)
	original := p.job
	defer original.Close()
	empty, err := OpenUnitJob()
	if err != nil {
		t.Fatal(err)
	}
	p.job = empty
	p.unassigned = true
	if err := p.Stop(time.Second); err != nil {
		t.Fatal("unassigned process cleanup", err)
	}
	if p.Alive() {
		t.Fatal("unassigned process survived cleanup")
	}
}
