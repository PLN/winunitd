package manager

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/PLN/winunitd/internal/runtime"
)

func TestUserTrackingLimitRetainsFailedCleanup(t *testing.T) {
	var starts atomic.Int32
	var procs []*failingUserMgr
	h := NewUserHost(UserHostConfig{
		Admission:      UserAdmission{Mode: "unit-files"},
		ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		QueryToken: func(id uint32) (*runtime.UserToken, error) {
			return &runtime.UserToken{Info: runtime.UserInfo{SID: fmt.Sprintf("S-1-5-21-1-2-3-%d", id+1000)}}, nil
		},
		Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			starts.Add(1)
			p := &failingUserMgr{}
			p.alive.Store(true)
			procs = append(procs, p)
			return p, nil
		},
	})
	t.Cleanup(h.Close)
	for i := uint32(1); i <= maxTrackedUserManagers; i++ {
		h.Logon(i)
	}
	procs[0].fail.Store(true)
	defer procs[0].fail.Store(false)
	h.Logoff(1)
	h.Logon(maxTrackedUserManagers + 1)
	if starts.Load() != maxTrackedUserManagers || h.ManagerCount() != maxTrackedUserManagers {
		t.Fatal("failed cleanup opened extra manager capacity")
	}
	procs[0].fail.Store(false)
	if err := h.stopUser(context.Background(), "S-1-5-21-1-2-3-1001", true); err != nil {
		t.Fatal(err)
	}
	h.Logon(maxTrackedUserManagers + 1)
	if starts.Load() != maxTrackedUserManagers+1 || h.ManagerCount() != maxTrackedUserManagers {
		t.Fatal("successful cleanup did not release manager capacity")
	}
}

func TestUserSessionSnapshotLimitPreservesOwnership(t *testing.T) {
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	h.Logon(1)
	h.cfg.Sessions = func() ([]uint32, error) { return make([]uint32, runtime.MaxInteractiveSessions+1), nil }
	h.Reconcile()
	if !h.Alive(testSIDA) || h.ManagerCount() != 1 {
		t.Fatal("oversized snapshot became an authoritative logoff")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[1] != testSIDA || len(h.sessionRequests) != 0 {
		t.Fatal("oversized snapshot changed session ownership")
	}
}

func TestUserSessionMappingLimit(t *testing.T) {
	var queries atomic.Int32
	h := NewUserHost(UserHostConfig{
		Admission:      UserAdmission{Mode: "unit-files"},
		ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		QueryToken: func(uint32) (*runtime.UserToken, error) {
			queries.Add(1)
			return &runtime.UserToken{Info: runtime.UserInfo{SID: testSIDA}}, nil
		},
		Start: func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			p := &fakeUserMgr{}
			p.alive.Store(true)
			return p, nil
		},
	})
	t.Cleanup(h.Close)
	for i := uint32(1); i <= runtime.MaxInteractiveSessions+4; i++ {
		h.Logon(i)
	}
	h.mu.Lock()
	count := len(h.sessions)
	h.mu.Unlock()
	if count != runtime.MaxInteractiveSessions || queries.Load() != runtime.MaxInteractiveSessions {
		t.Fatal("session mapping flood exceeded capacity")
	}
	h.Logoff(1)
	h.Logon(runtime.MaxInteractiveSessions + 1)
	if queries.Load() != runtime.MaxInteractiveSessions+1 {
		t.Fatal("logoff did not release session capacity")
	}
}
