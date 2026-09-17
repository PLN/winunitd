//go:build windows

package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows"
)

const overlapSession = 7

// suspendedLaunch holds a real UserHost launch between native creation and
// ResumeThread. Timestamps are logged so an ETW Kernel-Process trace of the same
// run can corroborate ProcessStart < event < ProcessStop for the reported PID.
type suspendedLaunch struct {
	host    *manager.UserHost
	sid     string
	starts  atomic.Int32
	closes  atomic.Int32
	entered chan int
	release chan struct{}
	logon   chan struct{}
}

func stamp(t *testing.T, event string, pid int) {
	t.Helper()
	t.Logf("%s %s pid=%d", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z"), event, pid)
}

func newSuspendedLaunch(t *testing.T) *suspendedLaunch {
	t.Helper()
	tok := runtime.NativeTestUserToken(t)
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	s := &suspendedLaunch{sid: tok.Info.SID, entered: make(chan int, 1), release: make(chan struct{}), logon: make(chan struct{})}
	s.host = manager.NewUserHost(manager.UserHostConfig{
		Admission:  manager.UserAdmission{Users: map[string]string{s.sid: "enabled"}},
		Exe:        exe,
		ExtraArgs:  []string{"-winunitd-helper=sleep"},
		QueryToken: func(uint32) (*runtime.UserToken, error) { return tok, nil },
		Sessions:   func() ([]uint32, error) { return []uint32{overlapSession}, nil },
		// The fixture owns the token; count the host's single release instead.
		CloseToken: func(*runtime.UserToken) error { s.closes.Add(1); return nil },
		Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			s.starts.Add(1)
			return runtime.StartUserManagerSuspended(spec, func(pid int) {
				s.entered <- pid
				<-s.release
			})
		},
		Logf: t.Logf,
	})
	go func() {
		defer close(s.logon)
		s.host.Logon(overlapSession)
	}()
	return s
}

// primaryThreadSuspendCount proves the created process has not run: its only
// thread reports a nonzero previous suspend count. The probe restores the count.
func primaryThreadSuspendCount(t *testing.T, pid int) uint32 {
	t.Helper()
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(snap)
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	threads := 0
	var count uint32
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		threads++
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			t.Fatal(err)
		}
		previous, err := suspendThread(th)
		if err != nil {
			windows.CloseHandle(th)
			t.Fatal(err)
		}
		if _, err := windows.ResumeThread(th); err != nil {
			windows.CloseHandle(th)
			t.Fatal(err)
		}
		windows.CloseHandle(th)
		count = previous
	}
	if threads != 1 {
		t.Fatalf("suspended process has %d threads, want 1", threads)
	}
	return count
}

var procSuspendThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("SuspendThread")

func suspendThread(h windows.Handle) (uint32, error) {
	r, _, err := procSuspendThread.Call(uintptr(h))
	if uint32(r) == 0xFFFFFFFF {
		return 0, err
	}
	return uint32(r), nil
}

func awaitSuspended(t *testing.T, s *suspendedLaunch) (int, windows.Handle) {
	t.Helper()
	var pid int
	select {
	case pid = <-s.entered:
	case <-s.logon:
		t.Fatal("logon finished without reaching native creation")
	case <-time.After(10 * time.Second):
		t.Fatal("native creation did not reach the suspended stage")
	}
	stamp(t, "created-suspended", pid)
	probe, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(probe) })
	if count := primaryThreadSuspendCount(t, pid); count != 1 {
		t.Fatalf("primary thread suspend count = %d, want 1", count)
	}
	if s.host.NativeWorkCount() != 1 || s.host.ManagerCount() != 1 {
		t.Fatalf("accepted launch not owned: native=%d managers=%d", s.host.NativeWorkCount(), s.host.ManagerCount())
	}
	return pid, probe
}

func assertBlocked(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s completed during native creation: %v", what, err)
	case <-time.After(200 * time.Millisecond):
	}
}

func assertSettled(t *testing.T, s *suspendedLaunch, pid int, probe windows.Handle) {
	t.Helper()
	select {
	case <-s.logon:
	case <-time.After(15 * time.Second):
		t.Fatal("logon worker did not finish")
	}
	if state, err := windows.WaitForSingleObject(probe, 10000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("superseded native process survived: state=%d error=%v", state, err)
	}
	stamp(t, "exit-observed", pid)
	if s.host.ManagerCount() != 0 || s.host.NativeWorkCount() != 0 || s.closes.Load() != 1 {
		t.Fatalf("retained state: managers=%d native=%d closes=%d", s.host.ManagerCount(), s.host.NativeWorkCount(), s.closes.Load())
	}
	// No resurrection: the session is still present, but neither the revoked
	// policy nor the closed host may relaunch during reconciliation.
	s.host.Reconcile()
	deadline := time.Now().Add(5 * time.Second)
	for s.host.NativeWorkCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(time.Second)
	if s.starts.Load() != 1 || s.host.ManagerCount() != 0 || s.host.NativeWorkCount() != 0 {
		t.Fatalf("resurrection: starts=%d managers=%d native=%d", s.starts.Load(), s.host.ManagerCount(), s.host.NativeWorkCount())
	}
}

func TestPolicyRevocationOverlapsNativeUserManagerCreation(t *testing.T) {
	s := newSuspendedLaunch(t)
	pid, probe := awaitSuspended(t, s)
	done := make(chan error, 1)
	stamp(t, "policy-revoke-issued", pid)
	go func() {
		done <- s.host.SetUserAdmission(manager.UserAdmission{Users: map[string]string{s.sid: "disabled"}})
	}()
	assertBlocked(t, done, "revocation")
	if primaryThreadSuspendCount(t, pid) != 1 {
		t.Fatal("process ran before revocation was decided")
	}
	stamp(t, "release", pid)
	close(s.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertSettled(t, s, pid, probe)
	if err := s.host.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownOverlapsNativeUserManagerCreation(t *testing.T) {
	s := newSuspendedLaunch(t)
	pid, probe := awaitSuspended(t, s)
	done := make(chan error, 1)
	stamp(t, "shutdown-issued", pid)
	go func() { done <- s.host.Shutdown(context.Background()) }()
	assertBlocked(t, done, "shutdown")
	if primaryThreadSuspendCount(t, pid) != 1 {
		t.Fatal("process ran before shutdown was decided")
	}
	stamp(t, "release", pid)
	close(s.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertSettled(t, s, pid, probe)
	s.host.Logon(overlapSession)
	if s.starts.Load() != 1 || s.host.ManagerCount() != 0 {
		t.Fatal("closed host accepted a later logon")
	}
}
