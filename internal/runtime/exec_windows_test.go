//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	env := helperEnv("WINUNITD_JOB_HELPER=" + mode)
	p, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Unit:         "test.service",
		Type:         typ,
		Argv:         []string{testAbs(t)},
		Dir:          t.TempDir(),
		Env:          env,
		TimeoutStart: timeout,
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
		Argv: []string{testAbs(t)},
		Dir:  t.TempDir(),
		Env:  helperEnv("WINUNITD_JOB_HELPER=oneshot"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if time.Since(start) > 5*time.Second {
		t.Fatal("oneshot hung")
	}
	if p.Alive() {
		t.Fatal("oneshot still running after Start")
	}
}

func TestOneshotTimeoutKillsJob(t *testing.T) {
	p, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Unit:         "oneshot.service",
		Type:         unit.TypeOneshot,
		Argv:         []string{testAbs(t)},
		Dir:          t.TempDir(),
		Env:          helperEnv("WINUNITD_JOB_HELPER=sleep"),
		TimeoutStart: 200 * time.Millisecond,
	})
	if err == nil {
		_ = p.Stop(time.Second)
		t.Fatal("expected TimeoutStartSec failure")
	}
	if p != nil {
		t.Fatal("process returned after timeout")
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
	_, err := DefaultLauncher().Start(context.Background(), StartSpec{
		Unit: "fail.service",
		Type: unit.TypeOneshot,
		Argv: []string{testAbs(t)},
		Dir:  t.TempDir(),
		Env:  helperEnv("WINUNITD_JOB_HELPER=fail"),
	})
	if err == nil {
		t.Fatal("expected non-zero oneshot to fail")
	}
	var st *ExitStatus
	if !errors.As(err, &st) || st.Code != 2 {
		t.Fatalf("err = %v", err)
	}
}

func TestStopKillsUnitJobTree(t *testing.T) {
	p := startHelper(t, "spawn", unit.TypeSimple, 0)
	line := readLine(t, p.Stdout(), 5*time.Second)
	child := parseChildPID(t, line)
	if err := p.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(p.PID()) && !processAlive(child) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("tree still alive after Stop parent=%v child=%v", processAlive(p.PID()), processAlive(child))
}
