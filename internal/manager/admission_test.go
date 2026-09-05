package manager

import (
	"path/filepath"
	"testing"

	"github.com/PLN/winunitd/internal/runtime"
)

func TestAdmissionPolicyDefaultsAndValidation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "user-admission.json")
	policy, err := LoadUserAdmission(p)
	if err != nil {
		t.Fatal(err)
	}
	if allow, probe := policy.decision(testSIDA); allow || probe {
		t.Fatal("missing policy admitted a user")
	}
	for _, raw := range []string{`{"mode":"all"}`, `{"mode":"unit-files","unknown":true}`, `{"users":{"invalid":"enabled"}}`, `{"users":{"S-1-5-21-1-2-3-1001":"yes"}}`, `{} {}`, `{"mode":"explicit","mode":"unit-files"}`, `{"mode":"explicit","Mode":"unit-files"}`, `{"users":{"S-1-5-21-1-2-3-1001":"disabled","S-1-5-21-1-2-3-1001":"enabled"}}`} {
		if _, err := parseUserAdmission([]byte(raw)); err == nil {
			t.Fatalf("invalid policy accepted: %s", raw)
		}
	}
	policy, err = (UserAdmission{Mode: "unit-files", Users: map[string]string{testSIDA: "disabled", testSIDB: "enabled"}}).validated()
	if err != nil {
		t.Fatal(err)
	}
	if allow, probe := policy.decision(testSIDA); allow || probe {
		t.Fatal("disable did not override delegation")
	}
	if allow, probe := policy.decision(testSIDB); !allow || probe {
		t.Fatal("explicit enable required files")
	}
}

func TestAdmissionDefaultAndExplicitEnable(t *testing.T) {
	h, starts, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	defer h.Close()
	if err := h.SetUserAdmission(UserAdmission{}); err != nil {
		t.Fatal(err)
	}
	h.Logon(1)
	if starts.Load() != 0 {
		t.Fatal("default admitted user")
	}
	p := UserAdmission{Users: map[string]string{testSIDA: "enabled"}}
	if err := h.SetUserAdmission(p); err != nil {
		t.Fatal(err)
	}
	p.Users[testSIDA] = "disabled"
	h.Logon(1)
	if starts.Load() != 1 {
		t.Fatal("explicit admission did not use immutable policy")
	}
	if err := h.SetUserAdmission(UserAdmission{}); err != nil {
		t.Fatal(err)
	}
	if h.Alive(testSIDA) {
		t.Fatal("revocation left interactive manager running")
	}
}

func TestAdmissionRejectsStaleFileProbe(t *testing.T) {
	h, starts, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	defer h.Close()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.cfg.ProbeUserUnits = func(*runtime.UserToken) (bool, error) { close(entered); <-release; return true, nil }
	go func() { h.Logon(1); close(done) }()
	<-entered
	if err := h.SetUserAdmission(UserAdmission{Mode: "unit-files", Users: map[string]string{testSIDA: "disabled"}}); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	<-done
	if starts.Load() != 0 {
		t.Fatal("stale probe bypassed explicit disable")
	}
}

func TestAdmissionFileRemovalPreservesLiveOwnership(t *testing.T) {
	h, starts, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	defer h.Close()
	h.Logon(1)
	h.cfg.ProbeUserUnits = func(*runtime.UserToken) (bool, error) { return false, nil }
	h.Logon(1)
	if starts.Load() != 1 || !h.Alive(testSIDA) {
		t.Fatal("file removal replaced or lost live manager")
	}
	h.Logoff(1)
	if h.Alive(testSIDA) {
		t.Fatal("file removal lost later logoff routing")
	}
	h.Logon(1)
	if starts.Load() != 1 {
		t.Fatal("empty directory readmitted user")
	}
}

func TestAdmissionRevocationKeepsOnlyExplicitLingerGrant(t *testing.T) {
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	defer h.Close()
	h.store = OpenLingerStore(t.TempDir())
	h.cfg.Lookup = func(string) (runtime.UserInfo, error) { return runtime.UserInfo{SID: testSIDA, Username: "alice"}, nil }
	h.Logon(1)
	if err := h.store.Put(runtime.LingerRecord{SID: testSIDA, Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := h.SetUserAdmission(UserAdmission{}); err != nil {
		t.Fatal(err)
	}
	if !h.Alive(testSIDA) {
		t.Fatal("interactive revocation removed independent linger grant")
	}
	if _, err := h.DisableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
	if h.Alive(testSIDA) {
		t.Fatal("revoked interactive admission kept manager after disabling linger")
	}
}

func TestAdmissionRevocationRetriesFailedCleanup(t *testing.T) {
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	p := &failingUserMgr{fakeUserMgr: fakeUserMgr{sid: testSIDA}}
	p.alive.Store(true)
	p.fail.Store(true)
	defer func() { p.fail.Store(false); h.Close() }()
	starts := 0
	h.cfg.Start = func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
		starts++
		return p, nil
	}
	h.Logon(1)
	if err := h.SetUserAdmission(UserAdmission{}); err == nil {
		t.Fatal("revocation concealed cleanup failure")
	}
	if h.ManagerCount() != 1 || !h.Alive(testSIDA) {
		t.Fatal("failed revocation discarded live ownership")
	}
	h.Logon(1)
	if starts != 1 {
		t.Fatal("revoked user launched a replacement")
	}
	p.fail.Store(false)
	if err := h.SetUserAdmission(UserAdmission{}); err != nil {
		t.Fatal(err)
	}
	if h.ManagerCount() != 0 || p.Alive() {
		t.Fatal("unchanged policy failed to retry retained cleanup")
	}
}

func TestAdmissionRevocationDuringLaunch(t *testing.T) {
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	defer h.Close()
	p := &fakeUserMgr{sid: testSIDA}
	p.alive.Store(true)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.cfg.Start = func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
		close(entered)
		<-release
		return p, nil
	}
	go func() { h.Logon(1); close(done) }()
	<-entered
	revoked := make(chan error, 1)
	go func() { revoked <- h.SetUserAdmission(UserAdmission{}) }()
	waitCond(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		allow, probe := h.admission.decision(testSIDA)
		return !allow && !probe
	})
	close(release)
	<-done
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if h.ManagerCount() != 0 || p.Alive() {
		t.Fatal("late launch survived admission revocation")
	}
}
