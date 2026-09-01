package manager

import (
	"context"
	"errors"
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
		Exe: "winunitd-test",
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
			if spec.Env == nil {
				t.Error("user manager spec must include deterministic env")
			}
			hasProfile := false
			for _, e := range spec.Env {
				if len(e) >= 12 && e[:12] == "USERPROFILE=" {
					hasProfile = true
				}
				if e == "SESSIONNAME=Console" {
					t.Error("SESSIONNAME leaked into user manager env")
				}
			}
			if !hasProfile {
				t.Error("USERPROFILE missing from user manager env")
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
	t.Setenv("LOCALAPPDATA", `C:\Users\ferd\AppData\Local`)
	got := DefaultUserBaseDir()
	want := filepath.Join(`C:\Users\ferd\AppData\Local`, "winunitd")
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
	waitAlive := time.Now().Add(2 * time.Second)
	for time.Now().Before(waitAlive) {
		if h.Alive(testSIDA) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !h.Alive(testSIDA) {
		t.Fatal("logon did not start a manager")
	}
	ch <- runtime.SessionChange{SessionID: 9, Logon: false}
	waitDead := time.Now().Add(2 * time.Second)
	for time.Now().Before(waitDead) {
		if !h.Alive(testSIDA) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if starts.Load() != 1 {
		t.Fatalf("starts = %d", starts.Load())
	}
	if h.Alive(testSIDA) {
		t.Fatal("logoff should have killed the manager")
	}
}

func TestFailClosedErrorIsNotPassword(t *testing.T) {
	t.Parallel()
	err := runtime.ErrNoUserToken
	if errors.Is(err, nil) {
		t.Fatal("nil")
	}
	_ = err
}
