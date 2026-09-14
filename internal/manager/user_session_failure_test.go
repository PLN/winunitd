package manager

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PLN/winunitd/internal/runtime"
)

func TestSessionProfileFailureVisibleBeforeIdentityAdmission(t *testing.T) {
	var unavailable atomic.Bool
	unavailable.Store(true)
	proc := &fakeUserMgr{}
	proc.sid = testSIDA
	h := NewUserHost(UserHostConfig{
		Admission: UserAdmission{Mode: "unit-files"},
		QueryToken: func(id uint32) (*runtime.UserToken, error) {
			if unavailable.Load() {
				return nil, errors.New("target profile: access is denied")
			}
			return &runtime.UserToken{Info: runtime.UserInfo{SID: testSIDA}, SessionID: id}, nil
		},
		ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		Start: func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			proc.alive.Store(true)
			return proc, nil
		},
	})
	t.Cleanup(h.Close)
	client := snapshotControlClient(t, &Control{Units: testManager(t, nil), Users: h})
	h.Logon(7)
	first := userSnapshotFromWire(t, client).UserHost
	if len(first.SessionFailures) != 1 || len(first.Instances) != 0 || len(first.Sessions) != 0 || first.NativeWork != 0 {
		t.Fatalf("pre-admission failure lost or granted ownership: %+v", first)
	}
	failure := first.SessionFailures[0]
	if failure.SessionID != 7 || failure.SID != "" || failure.Stage != "token-profile" || failure.Error != "target profile: access is denied" {
		t.Fatalf("wrong native failure context: %+v", failure)
	}
	first.SessionFailures[0].Error = "mutated"
	if userSnapshotFromWire(t, client).UserHost.SessionFailures[0] != failure {
		t.Fatal("failure snapshot aliases a published decision")
	}
	unavailable.Store(false)
	h.Logon(7)
	after := userSnapshotFromWire(t, client).UserHost
	if len(after.SessionFailures) != 0 || len(after.Instances) != 1 || len(after.Sessions) != 1 || after.Sessions[0].SID != testSIDA || !proc.Alive() {
		t.Fatalf("successful retry did not clear failure and launch: %+v", after)
	}
	h.Logoff(7)
	if len(userSnapshotFromWire(t, client).UserHost.SessionFailures) != 0 || proc.Alive() {
		t.Fatal("logoff retained failure or process ownership")
	}
}

func TestSessionFailureStagesAndNoUnitRecovery(t *testing.T) {
	for _, stage := range []string{"token-profile", "identity", "admission-probe"} {
		t.Run(stage, func(t *testing.T) {
			fail := true
			h := NewUserHost(UserHostConfig{
				Admission: UserAdmission{Mode: "unit-files"},
				QueryToken: func(id uint32) (*runtime.UserToken, error) {
					if fail && stage == "token-profile" {
						return nil, nil
					}
					sid := testSIDA
					if fail && stage == "identity" {
						sid = "invalid"
					}
					return &runtime.UserToken{Info: runtime.UserInfo{SID: sid}, SessionID: id}, nil
				},
				ProbeUserUnits: func(*runtime.UserToken) (bool, error) {
					if fail {
						return false, errors.New("unit directory is unavailable")
					}
					return false, nil
				},
				Start: func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
					t.Error("failure or absent units authorized a manager")
					return nil, errors.New("unexpected launch")
				},
			})
			t.Cleanup(h.Close)
			client := snapshotControlClient(t, &Control{Units: testManager(t, nil), Users: h})
			h.Logon(4)
			s := userSnapshotFromWire(t, client).UserHost
			if len(s.SessionFailures) != 1 || s.SessionFailures[0].Stage != stage || s.SessionFailures[0].Error == "" || len(s.Instances) != 0 {
				t.Fatalf("failure stage missing: %+v", s)
			}
			wantSID := ""
			if stage == "admission-probe" {
				wantSID = testSIDA
			}
			if s.SessionFailures[0].SID != wantSID {
				t.Fatal("failure used an identity that was not obtained from a valid token")
			}
			fail = false
			h.Logon(4)
			s = userSnapshotFromWire(t, client).UserHost
			if len(s.SessionFailures) != 0 || len(s.Instances) != 0 {
				t.Fatal("successful no-unit observation retained an error or admitted a manager")
			}
		})
	}
}

func TestLateSessionFailureCannotOutliveItsDecision(t *testing.T) {
	for _, decision := range []string{"logoff", "policy", "enumeration", "shutdown", "new-request"} {
		t.Run(decision, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var calls atomic.Int32
			h := NewUserHost(UserHostConfig{
				QueryToken: func(id uint32) (*runtime.UserToken, error) {
					if calls.Add(1) == 1 {
						close(entered)
						<-release
						return nil, errors.New("obsolete profile failure")
					}
					return &runtime.UserToken{Info: runtime.UserInfo{SID: testSIDA}, SessionID: id}, nil
				},
				Sessions: func() ([]uint32, error) { return nil, nil },
			})
			t.Cleanup(h.Close)
			client := snapshotControlClient(t, &Control{Units: testManager(t, nil), Users: h})
			done := make(chan struct{})
			go func() { h.Logon(7); close(done) }()
			awaitNativeWork(t, entered)
			switch decision {
			case "logoff":
				h.Logoff(7)
			case "policy":
				if err := h.SetUserAdmission(UserAdmission{Mode: "explicit", Users: map[string]string{testSIDA: "disabled"}}); err != nil {
					t.Fatal(err)
				}
			case "enumeration":
				h.Reconcile()
			case "shutdown":
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
				defer cancel()
				if err := h.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("pending native work was not retained: %v", err)
				}
			case "new-request":
				h.Logon(7)
			}
			unblock()
			awaitNativeWork(t, done)
			s := userSnapshotFromWire(t, client).UserHost
			if len(s.SessionFailures) != 0 || s.NativeWork != 0 || len(s.Instances) != 0 {
				t.Fatalf("obsolete failure resurrected: %+v", s)
			}
		})
	}
}

func TestSessionFailuresHaveBoundedRetentionAndWireDetails(t *testing.T) {
	var queries atomic.Int32
	h := NewUserHost(UserHostConfig{QueryToken: func(uint32) (*runtime.UserToken, error) {
		queries.Add(1)
		return nil, errors.New(strings.Repeat("\x00\"\\界", 1000))
	}})
	t.Cleanup(h.Close)
	client := snapshotControlClient(t, &Control{Units: testManager(t, nil), Users: h})
	for id := uint32(runtime.MaxInteractiveSessions); id > 0; id-- {
		h.Logon(id)
	}
	h.Logon(runtime.MaxInteractiveSessions + 1)
	s := userSnapshotFromWire(t, client).UserHost
	if queries.Load() != runtime.MaxInteractiveSessions || len(s.SessionFailures) != maxSnapshotSessionFailures || s.OmittedSessionFailures != runtime.MaxInteractiveSessions-maxSnapshotSessionFailures || len(s.Instances) != 0 {
		t.Fatalf("failure flood escaped its bounds: queries=%d details=%d omitted=%d", queries.Load(), len(s.SessionFailures), s.OmittedSessionFailures)
	}
	for i, failure := range s.SessionFailures {
		if failure.SessionID != uint32(i+1) || len(failure.Error) > maxSessionFailureBytes || !utf8.ValidString(failure.Error) || !strings.HasSuffix(failure.Error, "...") {
			t.Fatalf("unsorted or invalid bounded error: %+v", failure)
		}
	}
	h.Logoff(1)
	h.Logon(runtime.MaxInteractiveSessions + 1)
	if queries.Load() != runtime.MaxInteractiveSessions+1 {
		t.Fatal("logoff did not release failure observation capacity")
	}
	h.cfg.Sessions = func() ([]uint32, error) { return make([]uint32, runtime.MaxInteractiveSessions+1), nil }
	h.Reconcile()
	if len(userSnapshotFromWire(t, client).UserHost.SessionFailures) != maxSnapshotSessionFailures {
		t.Fatal("invalid enumeration removed retained failures")
	}
	h.cfg.Sessions = func() ([]uint32, error) { return nil, nil }
	h.Reconcile()
	s = userSnapshotFromWire(t, client).UserHost
	if len(s.SessionFailures) != 0 || s.OmittedSessionFailures != 0 {
		t.Fatal("authoritative empty enumeration retained failures")
	}
}
