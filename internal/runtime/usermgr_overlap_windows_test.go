//go:build windows

package runtime_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const overlapSession = 7

// suspendedLaunch holds a real UserHost launch between native creation and
// ResumeThread. Timestamps are logged so an ETW Kernel-Process trace of the same
// run can corroborate ProcessStart < event < ProcessStop for the reported PID.
type suspendedLaunch struct {
	host       *manager.UserHost
	control    *protocol.Client
	sid        string
	session    uint32
	headless   bool
	starts     atomic.Int32
	closes     atomic.Int32
	superseded atomic.Bool
	unblock    sync.Once
	entered    chan int
	release    chan struct{}
	logon      chan struct{}
}

func stamp(t *testing.T, event string, pid int) {
	t.Helper()
	t.Logf("%s %s pid=%d", time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z"), event, pid)
}

func newSuspendedLaunch(t *testing.T, headless bool) *suspendedLaunch {
	t.Helper()
	var tok *runtime.UserToken
	var session uint32
	var broker *runtime.DaemonJob
	if headless {
		tok, broker = headlessOverlapToken(t)
	} else {
		tok, session, broker = overlapToken(t)
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	s := &suspendedLaunch{sid: tok.Info.SID, session: session, headless: headless, entered: make(chan int, 1), release: make(chan struct{}), logon: make(chan struct{})}
	cfg := manager.UserHostConfig{
		Daemon:     broker,
		Admission:  manager.UserAdmission{Users: map[string]string{s.sid: "enabled"}},
		Exe:        exe,
		ExtraArgs:  []string{"-winunitd-helper=sleep"},
		QueryToken: func(uint32) (*runtime.UserToken, error) { return tok, nil },
		Sessions:   func() ([]uint32, error) { return []uint32{session}, nil },
		CloseToken: func(token *runtime.UserToken) error { s.closes.Add(1); return token.Close() },
		Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			s.starts.Add(1)
			return runtime.StartUserManagerSuspended(spec, func(pid int) {
				s.entered <- pid
				<-s.release
			})
		},
		Logf: func(format string, args ...any) {
			// Do not publish the fixture identity in ordinary CI logs.
			for _, arg := range args {
				if err, ok := arg.(error); ok && strings.Contains(err.Error(), "user manager launch superseded") {
					s.superseded.Store(true)
				}
			}
		},
	}
	if headless {
		cfg.LingerDir = t.TempDir()
		cfg.Sessions = func() ([]uint32, error) { return nil, nil }
		cfg.QueryToken = func(uint32) (*runtime.UserToken, error) {
			return nil, errors.New("headless fixture must not query WTS")
		}
		cfg.LingerToken = func(runtime.LingerRecord) (*runtime.UserToken, error) { return tok, nil }
	}
	s.host = manager.NewUserHost(cfg)
	units, err := manager.New(manager.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(units.Close)
	left, right := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		defer right.Close()
		protocol.ServeConn(ctx, right, &manager.Control{Units: units, Users: s.host}, protocol.AllowAdmin)
	}()
	t.Cleanup(func() { cancel(); left.Close(); <-served })
	s.control = protocol.NewClient(left)
	t.Cleanup(func() {
		s.resume()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.host.Shutdown(ctx); err != nil {
			t.Error("fixture shutdown:", err)
		}
		select {
		case <-s.logon:
		case <-ctx.Done():
			t.Error("fixture logon worker did not finish")
		}
	})
	go func() {
		defer close(s.logon)
		if headless {
			_, _ = s.host.EnableLinger(s.sid)
		} else {
			s.host.Logon(session)
		}
	}()
	return s
}

func (s *suspendedLaunch) resume() { s.unblock.Do(func() { close(s.release) }) }

// An explicit disposable fixture selects a genuine interactive token. Run only
// these tests in a dedicated SYSTEM process: broker self-assignment is permanent
// for that process, even after closing the job. The guest driver owns the logon,
// account, executable ACLs and restoration; no credentials enter this test.
func overlapToken(t *testing.T) (*runtime.UserToken, uint32, *runtime.DaemonJob) {
	t.Helper()
	value := os.Getenv("WINUNITD_NATIVE_OVERLAP_SESSION")
	if value == "" {
		var session uint32
		if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
			t.Fatal(err)
		}
		if session == 0 {
			t.Skip("interactive profile required; SYSTEM qualification sets WINUNITD_NATIVE_OVERLAP_SESSION")
		}
		return runtime.NativeTestUserToken(t), overlapSession, nil
	}
	if os.Getenv("WINUNITD_NATIVE_OVERLAP_FIXTURE") != "disposable" {
		t.Fatal("explicit disposable fixture required")
	}
	id, err := strconv.ParseUint(value, 10, 32)
	if err != nil || id == 0 {
		t.Fatal("nonzero WTS session required")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		t.Fatal("SYSTEM runner required")
	}
	tok, err := runtime.QueryUserToken(uint32(id))
	if tok != nil {
		t.Cleanup(func() {
			if err := tok.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	if sid := os.Getenv("WINUNITD_NATIVE_OVERLAP_SID"); sid == "" || tok.Info.SID != sid {
		t.Fatal("WTS token does not match the fixture SID")
	}
	broker, err := runtime.OpenBrokerJob()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := broker.AssignSelf(); err != nil {
		t.Fatal(err)
	}
	t.Logf("genuine WTS token selected: session=%d broker-job=self-assigned", id)
	return tok, uint32(id), broker
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
	var token windows.Token
	if err := windows.OpenProcessToken(probe, windows.TOKEN_QUERY, &token); err != nil {
		t.Fatal(err)
	}
	owner, err := token.GetTokenUser()
	elevated := token.IsElevated()
	token.Close()
	if err != nil || owner.User.Sid.String() != s.sid {
		t.Fatal("created process owner does not match the selected token")
	}
	if os.Getenv("WINUNITD_NATIVE_OVERLAP_FIXTURE") == "disposable" && elevated {
		t.Fatal("disposable fixture child must be a non-elevated user")
	}
	if s.headless || os.Getenv("WINUNITD_NATIVE_OVERLAP_SESSION") != "" {
		var session uint32
		if err := windows.ProcessIdToSessionId(uint32(pid), &session); err != nil || session != s.session {
			t.Fatal("created process did not enter the selected session")
		}
	}
	if s.headless && !overlapProfileLoaded(t, s.sid) {
		t.Fatal("headless profile was not loaded while native creation was held")
	}
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
	if !s.superseded.Load() {
		t.Fatal("launch did not report supersession by the overlapping decision")
	}
	if s.host.ManagerCount() != 0 || s.host.NativeWorkCount() != 0 || s.closes.Load() != 1 {
		t.Fatalf("retained state: managers=%d native=%d closes=%d", s.host.ManagerCount(), s.host.NativeWorkCount(), s.closes.Load())
	}
	// No resurrection: the session is still present, but neither the revoked
	// policy nor the closed host may relaunch during reconciliation.
	s.host.Reconcile()
	if s.headless {
		s.host.StartLingering()
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.host.NativeWorkCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(time.Second)
	if s.starts.Load() != 1 || s.host.ManagerCount() != 0 || s.host.NativeWorkCount() != 0 {
		t.Fatalf("resurrection: starts=%d managers=%d native=%d", s.starts.Load(), s.host.ManagerCount(), s.host.NativeWorkCount())
	}
	if s.headless {
		deadline := time.Now().Add(15 * time.Second)
		for overlapProfileLoaded(t, s.sid) {
			if time.Now().After(deadline) {
				t.Fatal("headless profile remained loaded after native cleanup")
			}
			time.Sleep(50 * time.Millisecond)
		}
		stamp(t, "profile-unloaded", pid)
	}
}

func TestPolicyRevocationOverlapsNativeUserManagerCreation(t *testing.T) {
	s := newSuspendedLaunch(t, false)
	pid, probe := awaitSuspended(t, s)
	revision := s.snapshot(t).AdmissionRevision
	done := make(chan error, 1)
	stamp(t, "policy-revoke-issued", pid)
	go func() {
		done <- s.host.SetUserAdmission(manager.UserAdmission{Users: map[string]string{s.sid: "disabled"}})
	}()
	s.awaitDecision(t, pid, func(view *protocol.UserHostSnapshot) bool { return view.AdmissionRevision > revision })
	assertBlocked(t, done, "revocation")
	if primaryThreadSuspendCount(t, pid) != 1 {
		t.Fatal("process ran before revocation was decided")
	}
	stamp(t, "release", pid)
	s.resume()
	if err := awaitOverlapResult(t, done); err != nil {
		t.Fatal(err)
	}
	assertSettled(t, s, pid, probe)
	if err := s.host.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownOverlapsNativeUserManagerCreation(t *testing.T) {
	s := newSuspendedLaunch(t, false)
	pid, probe := awaitSuspended(t, s)
	done := make(chan error, 1)
	stamp(t, "shutdown-issued", pid)
	go func() { done <- s.host.Shutdown(context.Background()) }()
	s.awaitDecision(t, pid, func(view *protocol.UserHostSnapshot) bool { return view.State == "closing" })
	assertBlocked(t, done, "shutdown")
	if primaryThreadSuspendCount(t, pid) != 1 {
		t.Fatal("process ran before shutdown was decided")
	}
	stamp(t, "release", pid)
	s.resume()
	if err := awaitOverlapResult(t, done); err != nil {
		t.Fatal(err)
	}
	assertSettled(t, s, pid, probe)
	s.host.Logon(s.session)
	if s.starts.Load() != 1 || s.host.ManagerCount() != 0 {
		t.Fatal("closed host accepted a later logon")
	}
}

func (s *suspendedLaunch) snapshot(t *testing.T) *protocol.UserHostSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snapshot, err := s.control.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.UserHost == nil {
		t.Fatal("missing user-host decision snapshot")
	}
	return snapshot.UserHost
}

func (s *suspendedLaunch) awaitDecision(t *testing.T, pid int, accepted func(*protocol.UserHostSnapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if accepted(s.snapshot(t)) {
			stamp(t, "decision-accepted-while-suspended", pid)
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("overlapping decision was not accepted while native creation was held")
}

func awaitOverlapResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("overlapping decision did not finish")
		return nil
	}
}

// Headless qualification is opt-in and must use a logged-off disposable local
// standard account. S4U supplies the token; no password or credential URI is used.
func headlessOverlapToken(t *testing.T) (*runtime.UserToken, *runtime.DaemonJob) {
	t.Helper()
	sid := os.Getenv("WINUNITD_NATIVE_OVERLAP_HEADLESS_SID")
	if sid == "" {
		t.Skip("requires disposable SYSTEM/headless-user qualification fixture")
	}
	if os.Getenv("WINUNITD_NATIVE_OVERLAP_FIXTURE") != "disposable" {
		t.Fatal("explicit disposable fixture required")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		t.Fatal("SYSTEM runner required")
	}
	if overlapProfileLoaded(t, sid) {
		t.Fatal("headless fixture profile must initially be unloaded")
	}
	tok, err := runtime.ObtainLingerToken(runtime.LingerRecord{SID: sid})
	if tok != nil {
		t.Cleanup(func() {
			if err := tok.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	if tok == nil || tok.Info.SID != sid || tok.Source != runtime.LingerTokenPathS4U {
		t.Fatal("genuine fixture S4U token required")
	}
	broker, err := runtime.OpenBrokerJob()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := broker.AssignSelf(); err != nil {
		t.Fatal(err)
	}
	t.Log("genuine S4U token selected: session=0 broker-job=self-assigned")
	return tok, broker
}

func overlapProfileLoaded(t *testing.T, sid string) bool {
	t.Helper()
	key, err := registry.OpenKey(registry.USERS, sid, registry.READ)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := key.Close(); err != nil {
		t.Fatal(err)
	}
	return true
}

func TestLingerRevocationOverlapsNativeHeadlessManagerCreation(t *testing.T) {
	s := newSuspendedLaunch(t, true)
	pid, probe := awaitSuspended(t, s)
	revision := s.snapshot(t).LingerRevision
	done := make(chan error, 1)
	stamp(t, "linger-revoke-issued", pid)
	go func() { _, err := s.host.DisableLinger(s.sid); done <- err }()
	s.awaitDecision(t, pid, func(view *protocol.UserHostSnapshot) bool { return view.LingerRevision > revision })
	assertBlocked(t, done, "linger revocation")
	if primaryThreadSuspendCount(t, pid) != 1 {
		t.Fatal("process ran before linger revocation was decided")
	}
	stamp(t, "release", pid)
	s.resume()
	if err := awaitOverlapResult(t, done); err != nil {
		t.Fatal(err)
	}
	assertSettled(t, s, pid, probe)
}

func TestShutdownOverlapsNativeHeadlessManagerCreation(t *testing.T) {
	testHeadlessShutdownOverlap(t, false)
}

func TestShutdownDeadlineOverlapsNativeHeadlessManagerCreation(t *testing.T) {
	testHeadlessShutdownOverlap(t, true)
}

func testHeadlessShutdownOverlap(t *testing.T, expire bool) {
	s := newSuspendedLaunch(t, true)
	pid, probe := awaitSuspended(t, s)
	budget := 20 * time.Second
	if expire {
		budget = time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	done := make(chan error, 1)
	stamp(t, "shutdown-issued", pid)
	go func() { done <- s.host.Shutdown(ctx) }()
	s.awaitDecision(t, pid, func(view *protocol.UserHostSnapshot) bool { return view.State == "closing" })
	assertBlocked(t, done, "shutdown")
	if expire {
		if err := awaitOverlapResult(t, done); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("held native shutdown deadline: %v", err)
		}
		if s.host.NativeWorkCount() != 1 || s.host.ManagerCount() != 1 || s.closes.Load() != 0 {
			t.Fatal("expired shutdown discarded accepted launch or token ownership")
		}
		stamp(t, "deadline-retains-native-ownership", pid)
	}
	if primaryThreadSuspendCount(t, pid) != 1 {
		t.Fatal("process ran before shutdown was decided")
	}
	stamp(t, "release", pid)
	s.resume()
	if expire {
		retry, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if err := s.host.Shutdown(retry); err != nil {
			t.Fatal(err)
		}
	} else if err := awaitOverlapResult(t, done); err != nil {
		t.Fatal(err)
	}
	assertSettled(t, s, pid, probe)
}
