package manager

import (
	"context"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestUserManagerModeDoesNotFollowLaterLogons(t *testing.T) {
	now := time.Now()
	h := NewUserHost(UserHostConfig{
		Now: func() time.Time { return now }, LingerDir: t.TempDir(),
		Admission:      UserAdmission{Mode: "unit-files"},
		ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		Lookup:         func(string) (runtime.UserInfo, error) { return runtime.UserInfo{SID: testSIDA}, nil },
		QueryToken: func(session uint32) (*runtime.UserToken, error) {
			return &runtime.UserToken{Info: runtime.UserInfo{SID: testSIDA}, SessionID: session}, nil
		},
		LingerToken: func(runtime.LingerRecord) (*runtime.UserToken, error) {
			return &runtime.UserToken{Info: runtime.UserInfo{SID: testSIDA}, Source: runtime.LingerTokenPathS4U}, nil
		},
		Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			p := &fakeUserMgr{sid: spec.SID}
			p.alive.Store(true)
			return p, nil
		},
	})
	defer h.Close()
	check := func(mode string, session uint32, count int) {
		t.Helper()
		s := h.decisionSnapshot()
		if s.running != 1 || len(s.instances) != 1 {
			t.Fatalf("snapshot: %+v", s)
		}
		u := s.instances[0]
		if u.Mode != mode || u.SessionID != session || u.InteractiveSessions != count || u.State != "running" || u.PID != 1 {
			t.Fatalf("user: %+v", u)
		}
	}
	if _, err := h.EnableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
	check("headless-s4u", 0, 0)
	h.Logon(7)
	check("headless-s4u", 0, 1)
	h.Logoff(7)
	check("headless-s4u", 0, 0)
	if _, err := h.DisableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
	if len(h.decisionSnapshot().instances) != 0 {
		t.Fatal("cleanup retained user status")
	}
	now = now.Add(2 * time.Second)
	h.Logon(9)
	check("interactive", 9, 1)
	if _, err := h.EnableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
	check("interactive", 9, 1)
	h.Logoff(9)
	check("interactive", 9, 0)
	if _, err := h.DisableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
}

type statusBlockedUser struct {
	fakeUserMgr
	release chan struct{}
}

func (p *statusBlockedUser) Alive() bool { <-p.release; return p.fakeUserMgr.Alive() }

func TestUserDecisionStatusIsCopiedAndDoesNotQueryProcesses(t *testing.T) {
	p := &statusBlockedUser{release: make(chan struct{})}
	p.sid = testSIDA
	p.alive.Store(true)
	h := NewUserHost(UserHostConfig{})
	defer h.Close()
	defer close(p.release)
	h.mu.Lock()
	h.bySID[testSIDA] = &userInstance{sid: testSIDA, proc: p, pid: 42, mode: "headless-s4u"}
	h.mu.Unlock()
	m := testManager(t, nil)
	c := &Control{Units: m, Users: h}
	done := make(chan *protocol.StatusResult, 1)
	go func() {
		result, err := c.Handle(context.Background(), protocol.MethodStatus, nil)
		if err != nil {
			done <- nil
			return
		}
		done <- result.(*protocol.StatusResult)
	}()
	select {
	case status := <-done:
		if status == nil || status.Machine.UserManagers != 1 || len(status.Machine.UserInstances) != 1 {
			t.Fatalf("status: %+v", status)
		}
		status.Machine.UserInstances[0].Mode = "changed"
		if h.decisionSnapshot().instances[0].Mode != "headless-s4u" {
			t.Fatal("snapshot mutated lifecycle state")
		}
	case <-time.After(time.Second):
		t.Fatal("status queried blocked process liveness")
	}
}
