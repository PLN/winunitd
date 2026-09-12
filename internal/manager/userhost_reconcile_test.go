package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
)

func TestUserHostReconcileMissedLogoff(t *testing.T) {
	h, starts, procs := testUserHost(t, map[uint32]string{1: testSIDA, 2: testSIDA}, nil)
	h.Logon(1)
	h.Logon(2)
	p := procs[testSIDA]
	h.cfg.Sessions = func() ([]uint32, error) { return []uint32{2}, nil }
	h.Reconcile()
	if !p.Alive() || starts.Load() != 1 {
		t.Fatal("remaining session lost its manager")
	}
	h.cfg.Sessions = func() ([]uint32, error) { return nil, nil }
	h.Reconcile()
	if p.Alive() || p.kills.Load() != 1 || h.ManagerCount() != 0 {
		t.Fatal("missed last logoff did not clean up the manager")
	}
}

func TestUserHostReconcileEnumerationFailurePreservesManager(t *testing.T) {
	h, _, procs := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	h.Logon(1)
	h.cfg.Sessions = func() ([]uint32, error) { return nil, errors.New("enumeration unavailable") }
	h.Reconcile()
	if !procs[testSIDA].Alive() {
		t.Fatal("failed enumeration was treated as an empty session list")
	}
}

func TestUserHostReconcileRetriesMissedLogoffCleanup(t *testing.T) {
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	p := &failingUserMgr{fakeUserMgr: fakeUserMgr{sid: testSIDA}}
	p.alive.Store(true)
	p.fail.Store(true)
	h.cfg.Start = func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) { return p, nil }
	t.Cleanup(func() { p.fail.Store(false) })
	h.Logon(1)
	h.Reconcile()
	if !h.Alive(testSIDA) || h.ManagerCount() != 1 {
		t.Fatal("failed cleanup discarded ownership")
	}
	p.fail.Store(false)
	h.Reconcile()
	if h.Alive(testSIDA) || h.ManagerCount() != 0 {
		t.Fatal("later enumeration did not retry retained cleanup")
	}
}

func TestUserHostReconcilePreservesLinger(t *testing.T) {
	h, _ := testLingerHost(t)
	if _, err := h.EnableLinger("alice"); err != nil {
		t.Fatal(err)
	}
	h.Logon(1)
	h.Reconcile()
	if !h.Alive(testSIDA) {
		t.Fatal("empty session enumeration revoked linger")
	}
	if _, err := h.DisableLinger("alice"); err != nil {
		t.Fatal(err)
	}
	if h.Alive(testSIDA) {
		t.Fatal("removed session retained manager after disabling linger")
	}
}

func TestUserHostReconcileSessionReplacement(t *testing.T) {
	for _, sameSID := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-user", true: "same-user"}[sameSID], func(t *testing.T) {
			sessions := map[uint32]string{1: testSIDA, 2: testSIDA}
			h, starts, procs := testUserHost(t, sessions, nil)
			h.Logon(1)
			old := procs[testSIDA]
			id := uint32(2)
			if !sameSID {
				id = 1 // reused Windows session ID now belongs to another user
				sessions[1] = testSIDB
			}
			h.cfg.Sessions = func() ([]uint32, error) { return []uint32{id, id}, nil }
			h.Reconcile()
			if sameSID {
				if !old.Alive() || starts.Load() != 1 {
					t.Fatal("replacement session unnecessarily restarted its manager")
				}
			} else if old.Alive() || !h.Alive(testSIDB) || h.ManagerCount() != 1 {
				t.Fatal("reused session ID retained the old user's manager")
			}
		})
	}
}

func TestUserHostReconcileCrashUsesFreshToken(t *testing.T) {
	h, starts, procs := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	query := h.cfg.QueryToken
	var tokens []*runtime.UserToken
	h.cfg.QueryToken = func(id uint32) (*runtime.UserToken, error) {
		tok, err := query(id)
		tokens = append(tokens, tok)
		return tok, err
	}
	h.Logon(1)
	old := procs[testSIDA]
	old.alive.Store(false)
	h.cfg.Sessions = func() ([]uint32, error) { return []uint32{1}, nil }
	h.Reconcile()
	if starts.Load() != 2 || !h.Alive(testSIDA) || len(tokens) != 2 || tokens[0] == tokens[1] || old.kills.Load() != 1 {
		t.Fatal("crash recovery did not reap the old process and obtain a fresh token")
	}
}

func TestUserHostReconcileRejectsStaleEnumeration(t *testing.T) {
	for _, event := range []string{"logon", "logoff", "policy", "shutdown", "reconcile"} {
		t.Run(event, func(t *testing.T) {
			h, starts, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
			h.Logon(1)
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			calls := 0
			h.cfg.Sessions = func() ([]uint32, error) {
				calls++
				if calls == 1 {
					close(entered)
					<-release
					if event == "logon" {
						return nil, nil
					}
					return []uint32{1}, nil
				}
				return nil, nil
			}
			go func() { h.Reconcile(); close(done) }()
			<-entered
			switch event {
			case "logon":
				h.Logon(1)
			case "logoff":
				h.Logoff(1)
			case "policy":
				if err := h.SetUserAdmission(UserAdmission{}); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
				err := h.Shutdown(ctx)
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("shutdown must retain the blocked enumeration: %v", err)
				}
			case "reconcile":
				h.Reconcile()
			}
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("reconciliation did not finish")
			}
			if h.Alive(testSIDA) != (event == "logon") || starts.Load() != 1 {
				t.Fatal("stale enumeration overrode a newer decision")
			}
		})
	}
}

func TestUserHostReconcileCancelsPendingTokenAfterMissedLogoff(t *testing.T) {
	h, starts, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	query := h.cfg.QueryToken
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.cfg.QueryToken = func(id uint32) (*runtime.UserToken, error) {
		close(entered)
		<-release
		return query(id)
	}
	go func() { h.Logon(1); close(done) }()
	<-entered
	h.Reconcile() // authoritative empty enumeration invalidates the token request
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("logon did not finish")
	}
	if starts.Load() != 0 || h.ManagerCount() != 0 {
		t.Fatal("late token resurrected a logged-off user")
	}
}
