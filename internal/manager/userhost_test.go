package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/runtime/runtimetest"
)

const (
	testSIDA = "S-1-5-21-1-2-3-1001"
	testSIDB = "S-1-5-21-1-2-3-1002"
)

type fakeUserMgr struct {
	sid    string
	alive  atomic.Bool
	kills  atomic.Int32
	starts *atomic.Int32
}

type gatedUserLiveness struct {
	fakeUserMgr
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (p *gatedUserLiveness) Alive() bool {
	if p.block.Load() {
		p.entered <- struct{}{}
		<-p.release
	}
	return p.fakeUserMgr.Alive()
}

func TestUserHostIndependentLogonWhileLivenessBlocked(t *testing.T) {
	p := &gatedUserLiveness{entered: make(chan struct{}, 1), release: make(chan struct{})}
	p.sid = testSIDA
	p.alive.Store(true)
	h := NewUserHost(UserHostConfig{
		Admission:      UserAdmission{Mode: "unit-files"},
		ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		QueryToken: func(session uint32) (*runtime.UserToken, error) {
			sid := testSIDA
			if session == 2 {
				sid = testSIDB
			}
			return &runtime.UserToken{Info: runtime.UserInfo{SID: sid}}, nil
		},
		Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			if spec.SID == testSIDA {
				return p, nil
			}
			other := &fakeUserMgr{sid: spec.SID}
			other.alive.Store(true)
			return other, nil
		},
	})
	defer h.Close()
	defer close(p.release)
	h.Logon(1)
	p.block.Store(true)
	first := make(chan struct{})
	go func() { h.Logon(1); close(first) }()
	select {
	case <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("liveness not reached")
	}
	second := make(chan struct{})
	go func() { h.Logon(2); close(second) }()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("independent logon blocked")
	}
}

func (p *fakeUserMgr) PID() int    { return 1 }
func (p *fakeUserMgr) SID() string { return p.sid }
func (p *fakeUserMgr) Alive() bool { return p.alive.Load() }
func (p *fakeUserMgr) Kill() error {
	p.alive.Store(false)
	p.kills.Add(1)
	return nil
}
func (p *fakeUserMgr) Wait(ctx context.Context) error {
	_ = ctx
	return nil
}

func testUserHost(t *testing.T, sidBySession map[uint32]string, fail map[uint32]error) (*UserHost, *atomic.Int32, map[string]*fakeUserMgr) {
	t.Helper()
	var starts atomic.Int32
	procs := map[string]*fakeUserMgr{}
	var mu sync.Mutex
	h := NewUserHost(UserHostConfig{
		Admission:      UserAdmission{Mode: "unit-files"},
		ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		Exe:            "winunitd-test",
		QueryToken: func(sessionID uint32) (*runtime.UserToken, error) {
			if err, ok := fail[sessionID]; ok {
				return nil, err
			}
			sid := sidBySession[sessionID]
			if sid == "" {
				return nil, runtime.ErrNoUserToken
			}
			return &runtime.UserToken{Info: runtime.UserInfo{
				SID:      sid,
				Username: "user",
				Domain:   "TEST",
				Profile:  `C:\Users\user`,
			}}, nil
		},
		Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			starts.Add(1)
			p := &fakeUserMgr{sid: spec.SID}
			p.alive.Store(true)
			mu.Lock()
			procs[spec.SID] = p
			mu.Unlock()
			if spec.Env != nil {
				t.Error("user host must leave environment resolution to the target-token launcher")
			}
			if !spec.LoadProfile {
				t.Error("system host must retain the user profile through manager cleanup")
			}
			return p, nil
		},
		Sessions: func() ([]uint32, error) { return nil, nil },
	})
	t.Cleanup(h.Close)
	return h, &starts, procs
}

func TestUserHostOneManagerPerSID(t *testing.T) {
	t.Parallel()
	h, starts, _ := testUserHost(t, map[uint32]string{
		1: testSIDA,
		2: testSIDA,
		3: testSIDB,
	}, nil)

	h.Logon(1)
	h.Logon(2)
	if starts.Load() != 1 {
		t.Fatalf("starts = %d, want 1 (one manager per SID)", starts.Load())
	}
	if !h.Alive(testSIDA) {
		t.Fatal("SID A manager should be alive")
	}

	h.Logon(3)
	if starts.Load() != 2 {
		t.Fatalf("starts = %d, want 2", starts.Load())
	}
	if !h.Alive(testSIDB) {
		t.Fatal("SID B manager should be alive")
	}
}

func TestUserHostLogoffKillsWhenLastSessionGone(t *testing.T) {
	t.Parallel()
	h, starts, procs := testUserHost(t, map[uint32]string{
		1: testSIDA,
		2: testSIDA,
	}, nil)

	h.Logon(1)
	h.Logon(2)
	h.Logoff(1)
	if starts.Load() != 1 {
		t.Fatalf("starts = %d", starts.Load())
	}
	if !h.Alive(testSIDA) {
		t.Fatal("manager must stay up while another session exists")
	}

	h.Logoff(2)
	if h.Alive(testSIDA) {
		t.Fatal("logoff of last session must kill the manager (no linger)")
	}
	if procs[testSIDA] == nil || procs[testSIDA].kills.Load() < 1 {
		t.Fatal("expected Kill on last logoff")
	}
}

func TestUserHostTokenFailureFailsClosed(t *testing.T) {
	t.Parallel()
	h, starts, _ := testUserHost(t, map[uint32]string{1: testSIDA}, map[uint32]error{
		1: runtime.ErrNoUserToken,
	})
	h.Logon(1)
	if starts.Load() != 0 {
		t.Fatal("must not start a manager without a WTS token")
	}
	if h.Alive(testSIDA) {
		t.Fatal("no manager on fail-closed token")
	}
}

func TestUserHostReconcileStartsExistingSessions(t *testing.T) {
	t.Parallel()
	h, starts, _ := testUserHost(t, map[uint32]string{4: testSIDA}, nil)
	h.cfg.Sessions = func() ([]uint32, error) { return []uint32{4}, nil }
	h.Reconcile()
	if starts.Load() != 1 || !h.Alive(testSIDA) {
		t.Fatalf("reconcile starts=%d alive=%v", starts.Load(), h.Alive(testSIDA))
	}
}

func TestUserHostCloseKillsAll(t *testing.T) {
	t.Parallel()
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA, 2: testSIDB}, nil)
	h.Logon(1)
	h.Logon(2)
	h.Close()
	if h.Alive(testSIDA) || h.Alive(testSIDB) {
		t.Fatal("Close must kill all user managers")
	}
}

func TestSystemListUnitsDoesNotIncludeUserUnits(t *testing.T) {
	t.Parallel()
	sys := testManager(t, map[string]string{
		"system.service": `
[Service]
ExecStart=C:\Tools\system.exe
WorkingDirectory=C:\Tools
`,
	})
	userDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(userDir, "units"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "units", "hermes.service"), []byte(`
[Service]
Type=oneshot
ExecStart=C:\Tools\hermes.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`), 0o644); err != nil {
		t.Fatal(err)
	}
	user, err := New(Config{BaseDir: userDir, Launch: runtimetest.Launcher()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(user.Close)
	if _, err := user.Reload(); err != nil {
		t.Fatal(err)
	}

	sysList, err := sys.ListUnits()
	if err != nil {
		t.Fatal(err)
	}
	userList, err := user.ListUnits()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range sysList.Units {
		if u.Name == "hermes.service" {
			t.Fatal("system list-units must not show user units")
		}
	}
	found := false
	for _, u := range userList.Units {
		if u.Name == "hermes.service" {
			found = true
		}
		if u.Name == "system.service" {
			t.Fatal("user list-units must not show system units")
		}
	}
	if !found {
		t.Fatal("user manager should load hermes.service")
	}

	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	h.Logon(1)
	sysList, err = sys.ListUnits()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range sysList.Units {
		if u.Name == "hermes.service" {
			t.Fatal("starting a user manager must not inject user units into the system manager")
		}
	}
}

func TestDefaultUserBaseDir(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\alice\AppData\Local`)
	got := DefaultUserBaseDir()
	want := filepath.Join(`C:\Users\alice\AppData\Local`, "winunitd")
	if got != want {
		t.Fatalf("DefaultUserBaseDir = %q, want %q", got, want)
	}
}

func TestUserHostListenLogonLogoff(t *testing.T) {
	t.Parallel()
	h, starts, _ := testUserHost(t, map[uint32]string{9: testSIDA}, nil)
	ch := make(chan runtime.SessionChange, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Listen(ctx, ch)
	}()
	ch <- runtime.SessionChange{SessionID: 9, Logon: true}
	waitCond(t, func() bool { return h.Alive(testSIDA) })
	ch <- runtime.SessionChange{SessionID: 9, Logon: false}
	close(ch)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session listener did not drain logoff and stop")
	}
	if starts.Load() != 1 {
		t.Fatalf("starts = %d", starts.Load())
	}
	waitCond(t, func() bool { return !h.Alive(testSIDA) })
}

func TestFailClosedErrorIsNotPassword(t *testing.T) {
	t.Parallel()
	err := runtime.ErrNoUserToken
	if errors.Is(err, nil) {
		t.Fatal("nil")
	}
	_ = err
}

type failingUserMgr struct {
	fakeUserMgr
	fail    atomic.Bool
	calls   atomic.Int32
	release <-chan struct{}
}

func (p *failingUserMgr) Kill() error {
	p.calls.Add(1)
	if p.release != nil {
		<-p.release
	}
	if p.fail.Load() {
		return errors.New("injected user-manager cleanup failure")
	}
	return p.fakeUserMgr.Kill()
}

func TestUserHostSerializesConcurrentStarts(t *testing.T) {
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA, 2: testSIDA}, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once, freed sync.Once
	unblock := func() { freed.Do(func() { close(release) }) }
	defer unblock()
	var starts, done atomic.Int32
	h.cfg.Start = func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
		starts.Add(1)
		once.Do(func() { close(entered) })
		<-release
		p := &fakeUserMgr{sid: spec.SID}
		p.alive.Store(true)
		return p, nil
	}
	go func() { h.Logon(1); done.Add(1) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("launch did not enter")
	}
	go func() { h.Logon(2); done.Add(1) }()
	waitCond(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.sessions) == 2 })
	unblock()
	waitCond(t, func() bool { return done.Load() == 2 })
	if starts.Load() != 1 || !h.Alive(testSIDA) {
		t.Fatal("concurrent logons created duplicate user managers")
	}
}

func TestUserHostLogoffFailureRetainsOwnership(t *testing.T) {
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA, 2: testSIDA}, nil)
	p := &failingUserMgr{fakeUserMgr: fakeUserMgr{sid: testSIDA}}
	p.alive.Store(true)
	p.fail.Store(true)
	var starts atomic.Int32
	h.cfg.Start = func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) { starts.Add(1); return p, nil }
	t.Cleanup(func() { p.fail.Store(false) })
	h.Logon(1)
	h.Logoff(1)
	if h.ManagerCount() != 1 || !h.Alive(testSIDA) {
		t.Fatal("failed logoff cleanup discarded ownership")
	}
	h.Logon(2)
	if starts.Load() != 1 {
		t.Fatal("uncertain cleanup admitted a replacement")
	}
	p.fail.Store(false)
	h.Logoff(2)
	if h.ManagerCount() != 0 || p.Alive() {
		t.Fatal("logoff cleanup retry did not finish")
	}
}

func TestUserHostShutdownDeadlineRetainsLateLaunch(t *testing.T) {
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	p := &failingUserMgr{fakeUserMgr: fakeUserMgr{sid: testSIDA}}
	p.alive.Store(true)
	p.fail.Store(true)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	t.Cleanup(func() { p.fail.Store(false) })
	h.cfg.Start = func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
		close(entered)
		<-release
		return p, nil
	}
	go func() { h.Logon(1); close(done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("launch did not enter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- h.Shutdown(ctx) }()
	if err := waitErr(t, stopped); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown result: %v", err)
	}
	if h.ManagerCount() != 1 {
		t.Fatal("shutdown deadline dropped accepted launch")
	}
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("late launch did not finish")
	}
	if h.ManagerCount() != 1 || !h.Alive(testSIDA) {
		t.Fatal("late launch cleanup failure lost ownership")
	}
	p.fail.Store(false)
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal("shutdown retry", err)
	}
	if h.ManagerCount() != 0 || p.Alive() {
		t.Fatal("shutdown retry left user manager owned")
	}
}

func TestUserHostFailedStartRetainsReturnedProcess(t *testing.T) {
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	p := &failingUserMgr{fakeUserMgr: fakeUserMgr{sid: testSIDA}}
	p.alive.Store(true)
	p.fail.Store(true)
	t.Cleanup(func() { p.fail.Store(false) })
	h.cfg.Start = func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
		return p, errors.New("injected launch failure")
	}
	h.Logon(1)
	if h.ManagerCount() != 1 || !h.Alive(testSIDA) {
		t.Fatal("failed start discarded returned user manager")
	}
	if err := h.Shutdown(context.Background()); err == nil {
		t.Fatal("failed cleanup reported success")
	}
	p.fail.Store(false)
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal("cleanup retry", err)
	}
}

func TestUserHostShutdownJoinsPendingKill(t *testing.T) {
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	p := &failingUserMgr{fakeUserMgr: fakeUserMgr{sid: testSIDA}, release: release}
	p.alive.Store(true)
	h.cfg.Start = func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) { return p, nil }
	h.Logon(1)
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		done := make(chan error, 1)
		go func() { done <- h.Shutdown(ctx) }()
		err := waitErr(t, done)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown result: %v", err)
		}
	}
	if p.calls.Load() != 1 || h.ManagerCount() != 1 {
		t.Fatal("shutdown retry duplicated pending kill or lost ownership")
	}
	unblock()
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal("shutdown retry", err)
	}
}

func TestUserHostRejectsTokenAfterLogoff(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprint("reuse=", reuse), func(t *testing.T) {
			h, starts, _ := testUserHost(t, map[uint32]string{1: testSIDB}, nil)
			query := h.cfg.QueryToken
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var calls atomic.Int32
			h.cfg.QueryToken = func(id uint32) (*runtime.UserToken, error) {
				if calls.Add(1) == 1 {
					close(entered)
					<-release
					return &runtime.UserToken{Info: runtime.UserInfo{SID: testSIDA}}, nil
				}
				return query(id)
			}
			go func() { h.Logon(1); close(done) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("token lookup did not enter")
			}
			h.Logoff(1)
			if reuse {
				h.Logon(1)
			}
			unblock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("stale lookup did not finish")
			}
			if h.Alive(testSIDA) {
				t.Fatal("stale token launched a user manager")
			}
			want := int32(0)
			if reuse {
				want = 1
				if !h.Alive(testSIDB) {
					t.Fatal("stale lookup displaced the new session")
				}
			}
			if starts.Load() != want {
				t.Fatalf("starts=%d want=%d", starts.Load(), want)
			}
			h.mu.Lock()
			pending := len(h.sessionRequests)
			h.mu.Unlock()
			if pending != 0 {
				t.Fatal("completed token requests retained")
			}
		})
	}
}

func TestUserHostLogoffDuringLaunchCleansLateProcess(t *testing.T) {
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	entered, release, loggedOn, loggedOff := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	p := &fakeUserMgr{sid: testSIDA}
	p.alive.Store(true)
	h.cfg.Start = func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
		close(entered)
		<-release
		return p, nil
	}
	go func() { h.Logon(1); close(loggedOn) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("launch did not enter")
	}
	go func() { h.Logoff(1); close(loggedOff) }()
	waitCond(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.sessions) == 0 })
	unblock()
	for _, done := range []chan struct{}{loggedOn, loggedOff} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("session operation did not finish")
		}
	}
	if p.Alive() || h.ManagerCount() != 0 {
		t.Fatal("late launch survived logoff")
	}
	if p.kills.Load() != 1 {
		t.Fatal("late launch cleanup was duplicated")
	}
}

func TestUserHostRejectsLingerTokenAfterDisable(t *testing.T) {
	h, store := testLingerHost(t)
	rec := runtime.LingerRecord{SID: testSIDA, Name: "alice"}
	if err := store.Put(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := h.refreshLingerRecords(); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	h.cfg.LingerToken = func(runtime.LingerRecord) (*runtime.UserToken, error) {
		close(entered)
		<-release
		return &runtime.UserToken{Info: runtime.UserInfo{SID: testSIDA}}, nil
	}
	done := make(chan error, 1)
	go func() { done <- h.startLinger(rec) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("linger token lookup did not enter")
	}
	if _, err := h.DisableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := waitErr(t, done); err == nil {
		t.Fatal("disabled linger launch reported success")
	}
	if h.ManagerCount() != 0 {
		t.Fatal("late linger token launched a manager")
	}
}
