//go:build windows

package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows"
)

// This opt-in test requires a disposable SYSTEM runner, a genuinely logged-on
// local standard user, and a private copy of the daemon executable readable by
// that user. Never point it at an installed or pilot executable. The fixture
// owns account/logon creation and restoration; ordinary CI skips this test.
func TestNativeUserLaunchOverlap(t *testing.T) {
	if os.Getenv("WINUNITD_NATIVE_LAUNCH_FIXTURE") != "disposable" {
		t.Skip("requires disposable SYSTEM/interactive-user qualification fixture")
	}
	sid, exe, base := os.Getenv("WINUNITD_NATIVE_LAUNCH_SID"), os.Getenv("WINUNITD_NATIVE_LAUNCH_EXE"), os.Getenv("WINUNITD_NATIVE_LAUNCH_BASE")
	if sid == "" || !filepath.IsAbs(exe) || !filepath.IsAbs(base) {
		t.Fatal("explicit fixture SID and absolute executable/base paths required")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		t.Fatal("SYSTEM runner required")
	}
	ids, err := runtime.InteractiveSessions()
	if err != nil {
		t.Fatal(err)
	}
	var session uint32
	for _, id := range ids {
		tok, err := runtime.QueryUserToken(id)
		if err != nil {
			t.Fatal(err)
		}
		if tok.Info.SID == sid {
			if session != 0 {
				t.Fatal("fixture requires exactly one native session for the target SID")
			}
			session = id
		}
		if err := tok.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if session == 0 {
		t.Fatal("target SID has no genuine WTS session")
	}
	broker, err := runtime.OpenBrokerJob()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := broker.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := broker.AssignSelf(); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"policy", "shutdown", "shutdown-deadline"} {
		t.Run(event, func(t *testing.T) {
			var launches atomic.Int32
			created := make(chan nativeLaunchObservation, 1)
			h := NewUserHost(UserHostConfig{
				Admission: UserAdmission{Mode: "explicit", Users: map[string]string{sid: "enabled"}},
				Exe:       exe, ExtraArgs: []string{"--base-dir", base}, Daemon: broker,
				// Observation only: all token/profile/environment/process/job work
				// uses the production implementation, without an adapter delay.
				Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
					launches.Add(1)
					proc, err := runtime.StartUserManager(spec)
					seen := nativeLaunchObservation{err: err}
					if proc != nil {
						seen.pid = proc.PID()
						var observeErr error
						seen.handle, observeErr = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(proc.PID()))
						if observeErr == nil {
							observeErr = verifyNativeLaunchIdentity(seen.handle, uint32(proc.PID()), sid, session)
						}
						seen.err = errors.Join(err, observeErr)
					}
					created <- seen
					return proc, err
				},
			})
			lock := acquireNativeLaunchOplock(t, exe)
			defer func() {
				lock.close(t)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if err := h.Shutdown(ctx); err != nil {
					t.Error("fixture shutdown", err)
				}
			}()
			reconciled := make(chan struct{})
			go func() { h.Reconcile(); close(reconciled) }()
			if state, err := windows.WaitForSingleObject(lock.event, 15000); err != nil || state != windows.WAIT_OBJECT_0 {
				t.Fatalf("native image open did not break oplock: state=%d err=%v", state, err)
			}
			assertNativeCreateBlocked(t, created)
			changed := make(chan error, 1)
			if event == "policy" {
				go func() { changed <- h.SetUserAdmission(UserAdmission{}) }()
			} else {
				budget := 20 * time.Second
				if event == "shutdown-deadline" {
					budget = 200 * time.Millisecond
				}
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), budget)
					defer cancel()
					changed <- h.Shutdown(ctx)
				}()
			}
			until := time.Now().Add(5 * time.Second)
			accepted := false
			for time.Now().Before(until) {
				h.mu.Lock()
				allow, probe := h.admission.decision(sid)
				accepted = (event == "policy" && !allow && !probe) || (event != "policy" && h.closed)
				h.mu.Unlock()
				if accepted {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if !accepted {
				t.Fatal("policy/shutdown was not accepted while native creation was blocked")
			}
			assertNativeCreateBlocked(t, created)
			if event == "shutdown-deadline" {
				if err := nativeLaunchResult(t, changed); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("blocked shutdown deadline: %v", err)
				}
				if h.NativeWorkCount() == 0 || h.ManagerCount() != 1 {
					t.Fatal("expired shutdown dropped accepted native launch ownership")
				}
				assertNativeCreateBlocked(t, created)
			}
			lock.close(t)
			select {
			case <-reconciled:
			case <-time.After(20 * time.Second):
				t.Fatal("native reconciliation did not finish after oplock release")
			}
			seen := <-created
			if seen.handle != 0 {
				defer windows.CloseHandle(seen.handle)
			}
			if seen.err != nil || seen.handle == 0 || seen.pid <= 0 {
				t.Fatalf("late native process observation: pid=%d err=%v", seen.pid, seen.err)
			}
			if event != "shutdown-deadline" {
				if err := nativeLaunchResult(t, changed); err != nil {
					t.Fatal(err)
				}
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				err := h.Shutdown(ctx)
				cancel()
				if err != nil {
					t.Fatal("shutdown retry", err)
				}
			}
			if state, err := windows.WaitForSingleObject(seen.handle, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
				t.Fatalf("late native process survived: state=%d err=%v", state, err)
			}
			until = time.Now().Add(12 * time.Second)
			for time.Now().Before(until) {
				h.Reconcile()
				if h.ManagerCount() != 0 || h.NativeWorkCount() != 0 || launches.Load() != 1 {
					t.Fatal("late completion resurrected a manager or retained native work")
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Logf("native overlap=%s session=%d latePID=%d exited=true noResurrection=12s", event, session, seen.pid)
		})
	}
}

type nativeLaunchObservation struct {
	pid    int
	handle windows.Handle
	err    error
}

func verifyNativeLaunchIdentity(handle windows.Handle, pid uint32, sid string, session uint32) error {
	var actual uint32
	if err := windows.ProcessIdToSessionId(pid, &actual); err != nil || actual != session {
		return fmt.Errorf("native process session=%d expected=%d: %w", actual, session, err)
	}
	var token windows.Token
	if err := windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	if user.User.Sid.String() != sid || token.IsElevated() {
		return fmt.Errorf("native process must have the fixture's non-elevated user identity")
	}
	return nil
}

func nativeLaunchResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(25 * time.Second):
		t.Fatal("native operation did not finish")
		return nil
	}
}

func assertNativeCreateBlocked(t *testing.T, created <-chan nativeLaunchObservation) {
	t.Helper()
	select {
	case seen := <-created:
		if seen.handle != 0 {
			windows.CloseHandle(seen.handle)
		}
		t.Fatalf("creation returned before the native overlap boundary: %v", seen.err)
	default:
	}
	stack := make([]byte, 2<<20)
	n := goruntime.Stack(stack, true)
	if n == len(stack) {
		t.Fatal("truncated native stack evidence")
	}
	for _, g := range strings.Split(string(stack[:n]), "\n\n") {
		if strings.Contains(g, "golang.org/x/sys/windows.CreateProcessAsUser(") && strings.Contains(g, "runtime.createUserManagerWithBoundary(") {
			t.Logf("native creation held by image oplock:\n%s", g)
			return
		}
	}
	t.Fatal("oplock break alone does not prove a blocked CreateProcessAsUser call")
}

type nativeLaunchOplock struct {
	file, event windows.Handle
	overlapped  *windows.Overlapped
	returned    uint32
}

func acquireNativeLaunchOplock(t *testing.T, exe string) *nativeLaunchOplock {
	t.Helper()
	path, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		t.Fatal(err)
	}
	f, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		t.Fatal(err)
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(f)
		t.Fatal(err)
	}
	lock := &nativeLaunchOplock{file: f, event: event, overlapped: &windows.Overlapped{HEvent: event}}
	// FSCTL_REQUEST_OPLOCK_LEVEL_1. Windows holds a conflicting open until
	// acknowledgment or close; no code is injected into the launching process.
	if err := windows.DeviceIoControl(f, 0x00090000, nil, 0, nil, 0, &lock.returned, lock.overlapped); err != windows.ERROR_IO_PENDING {
		windows.CloseHandle(f)
		lock.file = 0
		lock.close(t)
		t.Fatalf("exclusive image oplock was not granted: %v", err)
	}
	return lock
}

func (lock *nativeLaunchOplock) close(t *testing.T) {
	t.Helper()
	if lock.file != 0 {
		if err := windows.CancelIoEx(lock.file, lock.overlapped); err != nil && err != windows.ERROR_NOT_FOUND {
			t.Error("cancel native oplock observation", err)
		}
		if err := windows.GetOverlappedResult(lock.file, lock.overlapped, &lock.returned, true); err != nil && err != windows.ERROR_OPERATION_ABORTED {
			t.Error("join native oplock observation", err)
		}
		if err := windows.CloseHandle(lock.file); err != nil {
			t.Error("release native image oplock", err)
		}
		lock.file = 0
		goruntime.KeepAlive(lock.overlapped)
	}
	if lock.event != 0 {
		if err := windows.CloseHandle(lock.event); err != nil {
			t.Error(err)
		}
		lock.event = 0
	}
}
