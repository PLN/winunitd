package manager

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
)

func TestUserRecoveryBackoffCapsAndReleasesOnLogoff(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var starts, queries atomic.Int32
	h := NewUserHost(UserHostConfig{
		Now:            func() time.Time { return now },
		Admission:      UserAdmission{Mode: "unit-files"},
		ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		QueryToken: func(uint32) (*runtime.UserToken, error) {
			queries.Add(1)
			return &runtime.UserToken{Info: runtime.UserInfo{SID: testSIDA}}, nil
		},
		Start: func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			starts.Add(1)
			return nil, errors.New("injected launch failure")
		},
	})
	t.Cleanup(h.Close)
	for i, delay := range []time.Duration{1, 2, 4, 8, 16, 32, 60, 60} {
		h.Logon(1)
		if starts.Load() != int32(i+1) {
			t.Fatal("due recovery did not launch")
		}
		r := h.RecoveryStatus()
		if len(r) != 1 || r[0].State != "waiting" || r[0].NextAttemptAt != now.Add(delay*time.Second).Format(time.RFC3339Nano) || r[0].Error != "injected launch failure" {
			t.Fatalf("wrong recovery decision: %+v", r)
		}
		for j := 0; j < 100; j++ {
			h.Logon(1)
		}
		if starts.Load() != int32(i+1) || queries.Load() != int32(i+1) {
			t.Fatal("notification storm bypassed recovery delay")
		}
		now = now.Add(delay * time.Second)
	}
	h.Logoff(1)
	if h.ManagerCount() != 0 || len(h.RecoveryStatus()) != 0 {
		t.Fatal("logoff retained a canceled recovery")
	}
	h.Logon(1)
	if starts.Load() != 9 {
		t.Fatal("new logon inherited canceled recovery")
	}
}

func TestUserRecoveryResetsAfterStableRuntime(t *testing.T) {
	h, starts, procs := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.cfg.Now = func() time.Time { return now }
	h.Logon(1)
	procs[testSIDA].alive.Store(false)
	h.Logon(1)
	if starts.Load() != 1 || h.ManagerCount() != 1 || h.RecoveryStatus()[0].State != "waiting" {
		t.Fatal("immediate crash bypassed backoff")
	}
	now = now.Add(time.Second)
	h.Logon(1)
	if starts.Load() != 2 {
		t.Fatal("due crash recovery did not start")
	}
	now = now.Add(userRecoveryStableTime)
	procs[testSIDA].alive.Store(false)
	h.Logon(1)
	if starts.Load() != 3 {
		t.Fatal("stable runtime recovery did not start")
	}
	h.mu.Lock()
	delay := h.bySID[testSIDA].restartDelay
	h.mu.Unlock()
	if delay != userRecoveryMinDelay {
		t.Fatal("stable runtime did not reset restart delay")
	}
}

func TestUserRecoveryRevocationAndShutdownCancelWaiting(t *testing.T) {
	for _, action := range []string{"revoke", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			h, starts, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
			h.cfg.Start = func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
				starts.Add(1)
				return nil, errors.New("failed")
			}
			h.Logon(1)
			if action == "revoke" {
				if err := h.SetUserAdmission(UserAdmission{}); err != nil {
					t.Fatal(err)
				}
			} else if err := h.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			h.Logon(1)
			if h.ManagerCount() != 0 || starts.Load() != 1 {
				t.Fatal("canceled recovery was resurrected")
			}
		})
	}
}

func TestUserRecoveryPeriodicLingerScan(t *testing.T) {
	h, _ := testLingerHost(t)
	var starts atomic.Int32
	launch := h.cfg.Start
	h.cfg.Start = func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
		starts.Add(1)
		return launch(spec)
	}
	base := time.Now()
	var elapsed atomic.Int64
	h.cfg.Now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	if _, err := h.EnableLinger("alice"); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	inst := h.bySID[testSIDA]
	inst.proc.(*fakeUserMgr).alive.Store(false)
	h.mu.Unlock()
	elapsed.Store(int64(userRecoveryMinDelay))
	h.scheduleReconcile()
	waitCond(t, func() bool { return starts.Load() == 2 && h.Alive(testSIDA) })
	if _, err := h.DisableLinger("alice"); err != nil {
		t.Fatal(err)
	}
	h.scheduleReconcile()
	waitCond(t, func() bool { return h.NativeWorkCount() == 0 })
	if h.ManagerCount() != 0 || starts.Load() != 2 {
		t.Fatal("disabled linger recovered again")
	}
}

func TestUserRecoveryLingerTokenFailureBackoff(t *testing.T) {
	h, _ := testLingerHost(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.cfg.Now = func() time.Time { return now }
	var calls atomic.Int32
	h.cfg.LingerToken = func(runtime.LingerRecord) (*runtime.UserToken, error) {
		calls.Add(1)
		return nil, runtime.ErrNoLingerToken
	}
	if _, err := h.EnableLinger("alice"); err != nil {
		t.Fatal(err)
	}
	if h.Alive(testSIDA) {
		t.Fatal("failed token acquisition created a manager")
	}
	for i := 0; i < 100; i++ {
		h.StartLingering()
	}
	if calls.Load() != 1 || len(h.RecoveryStatus()) != 1 {
		t.Fatal("linger token failures bypassed backoff")
	}
	now = now.Add(time.Second)
	h.StartLingering()
	if calls.Load() != 2 {
		t.Fatal("due linger token retry did not run")
	}
	if _, err := h.DisableLinger("alice"); err != nil {
		t.Fatal(err)
	}
	if h.ManagerCount() != 0 {
		t.Fatal("disable-linger retained token recovery")
	}
}
