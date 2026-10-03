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
	// Reconciliation confirms the manager is still running a minute later.
	now = now.Add(userRecoveryStableTime)
	h.Logon(1)
	procs[testSIDA].alive.Store(false)
	h.Logon(1)
	if starts.Load() != 3 {
		t.Fatal("stable runtime recovery did not start")
	}
	if delay := userRestartDelay(h, testSIDA); delay != userRecoveryMinDelay {
		t.Fatalf("confirmed stable runtime did not reset restart delay: %v", delay)
	}
}

// #254: a manager that is created successfully but exits at once must keep
// its capped backoff, however long recovery waits after the exit.
func TestUserRecoveryImmediateExitKeepsCappedBackoff(t *testing.T) {
	h, starts, procs := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.cfg.Now = func() time.Time { return now }
	for i, delay := range []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60} {
		delay *= time.Second
		h.Logon(1)
		if starts.Load() != int32(i+1) {
			t.Fatalf("launch %d did not start", i+1)
		}
		if got := userRestartDelay(h, testSIDA); got != delay {
			t.Fatalf("launch %d delay = %v, want %v", i+1, got, delay)
		}
		launched := now
		procs[testSIDA].alive.Store(false)
		h.Logon(1)
		r := h.RecoveryStatus()
		if starts.Load() != int32(i+1) || len(r) != 1 || r[0].State != "waiting" || r[0].NextAttemptAt != launched.Add(delay).Format(time.RFC3339Nano) {
			t.Fatalf("launch %d exit: starts %d, recovery %+v", i+1, starts.Load(), r)
		}
		// Recovery runs long after it is due, well beyond the stable threshold.
		now = launched.Add(delay + 3*userRecoveryStableTime)
	}
}

// Only observed running time counts: a manager last seen alive shortly after
// launch does not reset its backoff, even when it is relaunched much later.
func TestUserRecoveryCountsOnlyObservedRuntime(t *testing.T) {
	h, starts, procs := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.cfg.Now = func() time.Time { return now }
	h.Logon(1)
	now = now.Add(userRecoveryStableTime / 2)
	h.Logon(1) // observed alive at half the threshold
	procs[testSIDA].alive.Store(false)
	now = now.Add(10 * userRecoveryStableTime)
	h.Logon(1)
	if starts.Load() != 2 {
		t.Fatal("due recovery did not start")
	}
	if delay := userRestartDelay(h, testSIDA); delay != 2*userRecoveryMinDelay {
		t.Fatalf("unobserved runtime reset the delay: %v", delay)
	}
}

// Lingering managers are observed by the linger scan, not by session logons.
func TestUserRecoveryLingerScanConfirmsRuntime(t *testing.T) {
	for _, observed := range []bool{false, true} {
		h, _ := testLingerHost(t)
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		h.cfg.Now = func() time.Time { return now }
		if _, err := h.EnableLinger("alice"); err != nil {
			t.Fatal(err)
		}
		h.mu.Lock()
		first := h.bySID[testSIDA]
		h.mu.Unlock()
		if first == nil || first.proc == nil {
			t.Fatal("linger did not start a manager")
		}
		now = now.Add(userRecoveryStableTime)
		if observed {
			h.StartLingering()
		}
		first.proc.(*fakeUserMgr).alive.Store(false)
		now = now.Add(time.Minute)
		h.StartLingering()
		h.mu.Lock()
		next := h.bySID[testSIDA]
		h.mu.Unlock()
		if next == nil || next == first || next.proc == nil {
			t.Fatalf("observed=%t: linger scan did not relaunch", observed)
		}
		want := 2 * userRecoveryMinDelay
		if observed {
			want = userRecoveryMinDelay
		}
		if delay := userRestartDelay(h, testSIDA); delay != want {
			t.Fatalf("observed=%t: delay %v, want %v", observed, delay, want)
		}
	}
}

func userRestartDelay(h *UserHost, sid string) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bySID[sid].restartDelay
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

// A liveness result for a replaced process or instance is not running time.
func TestUserRecoveryIgnoresStaleLivenessObservation(t *testing.T) {
	h, _, procs := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.cfg.Now = func() time.Time { return now }
	h.Logon(1)
	h.mu.Lock()
	inst := h.bySID[testSIDA]
	h.mu.Unlock()
	now = now.Add(userRecoveryStableTime)
	h.observeUserAlive(testSIDA, inst, &fakeUserMgr{sid: testSIDA}, now)
	h.observeUserAlive(testSIDA, &userInstance{startedAt: inst.startedAt, proc: inst.proc}, inst.proc, now)
	h.observeUserAlive(testSIDA, inst, procs[testSIDA], inst.startedAt.Add(-time.Second))
	h.mu.Lock()
	stale := inst.aliveAt
	h.mu.Unlock()
	if !stale.IsZero() {
		t.Fatalf("stale observation recorded at %v", stale)
	}
	h.observeUserAlive(testSIDA, inst, procs[testSIDA], now)
	h.mu.Lock()
	current := inst.aliveAt
	h.mu.Unlock()
	if !current.Equal(now) {
		t.Fatalf("current observation = %v, want %v", current, now)
	}
}

// sampledLiveness samples the process state first and, while held, returns
// that sample only when released, like a slow native liveness query.
type sampledLiveness struct {
	fakeUserMgr
	hold    atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (p *sampledLiveness) Alive() bool {
	alive := p.fakeUserMgr.Alive()
	if p.hold.Load() {
		p.entered <- struct{}{}
		<-p.release
	}
	return alive
}

// A positive liveness result that returns after the process exited must not
// credit the time after the exit (#254): the check counts from its start.
func TestUserRecoveryDelayedLivenessCountsSampleTime(t *testing.T) {
	for _, path := range []string{"session", "linger"} {
		t.Run(path, func(t *testing.T) {
			var h *UserHost
			if path == "session" {
				h, _, _ = testUserHost(t, map[uint32]string{1: testSIDA}, nil)
			} else {
				h, _ = testLingerHost(t)
			}
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			var offset atomic.Int64
			h.cfg.Now = func() time.Time { return base.Add(time.Duration(offset.Load())) }
			slow := &sampledLiveness{entered: make(chan struct{}), release: make(chan struct{})}
			slow.sid = testSIDA
			slow.alive.Store(true)
			var starts atomic.Int32
			h.cfg.Start = func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
				if starts.Add(1) == 1 {
					return slow, nil
				}
				p := &fakeUserMgr{sid: spec.SID}
				p.alive.Store(true)
				return p, nil
			}
			check := func() {
				if path == "session" {
					h.Logon(1)
				} else {
					h.StartLingering()
				}
			}
			if path == "session" {
				h.Logon(1)
			} else if _, err := h.EnableLinger("alice"); err != nil {
				t.Fatal(err)
			}
			if starts.Load() != 1 {
				t.Fatal("manager did not start")
			}
			// A check samples the manager alive ten seconds after launch...
			offset.Store(int64(10 * time.Second))
			slow.hold.Store(true)
			done := make(chan struct{})
			go func() { defer close(done); check() }()
			select {
			case <-slow.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("liveness check did not start")
			}
			// ...but its result returns only after the manager exited and three
			// minutes passed.
			slow.alive.Store(false)
			offset.Store(int64(3 * time.Minute))
			slow.hold.Store(false)
			close(slow.release)
			<-done
			check()
			if starts.Load() != 2 {
				t.Fatalf("recovery did not relaunch: %d starts", starts.Load())
			}
			if delay := userRestartDelay(h, testSIDA); delay != 2*userRecoveryMinDelay {
				t.Fatalf("ten seconds of observed runtime reset the delay: %v", delay)
			}
		})
	}
}
