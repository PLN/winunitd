package manager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

const aliceSID = "S-1-5-21-1-2-3-1001"

// sessionLauncher reports a logon for every workload it starts, as the Windows
// launcher does for a User= process, and can hold the first workload start.
type sessionLauncher struct {
	cooperativeLauncher
	logonMu  sync.Mutex
	logon    string
	session  uint32
	entered  chan struct{} // closed when the held start is reached
	hold     chan struct{} // the first workload start waits for it
	startErr error         // the first workload start fails with it
	killJob  bool          // killing the unit job ends the main process
}

func (l *sessionLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if strings.Contains(spec.Argv[0], "stop") {
		return l.cooperativeLauncher.Start(ctx, spec)
	}
	l.logonMu.Lock()
	hold, entered, startErr := l.hold, l.entered, l.startErr
	l.hold, l.entered, l.startErr = nil, nil, nil
	logon, session, killJob := l.logon, l.session, l.killJob
	l.logonMu.Unlock()
	if hold != nil {
		close(entered)
		<-hold
	}
	if startErr != nil {
		l.fakeLauncher.mu.Lock()
		l.fakeLauncher.starts = append(l.fakeLauncher.starts, spec)
		l.fakeLauncher.mu.Unlock()
		return nil, startErr
	}
	p, err := l.cooperativeLauncher.Start(ctx, spec)
	if err != nil {
		return p, err
	}
	return &logonProc{fakeProc: p.(*fakeProc), logon: logon, session: session, killJob: killJob}, nil
}

func (l *sessionLauncher) workloads() []runtime.StartSpec {
	var out []runtime.StartSpec
	for _, spec := range l.specs() {
		if !strings.Contains(spec.Argv[0], "stop") {
			out = append(out, spec)
		}
	}
	return out
}

func (l *sessionLauncher) helpers() []runtime.StartSpec {
	var out []runtime.StartSpec
	for _, spec := range l.specs() {
		if strings.Contains(spec.Argv[0], "stop") {
			out = append(out, spec)
		}
	}
	return out
}

type logonProc struct {
	*fakeProc
	logon   string
	session uint32
	killJob bool
}

func (p *logonProc) AccountLogon() (string, uint32) { return p.logon, p.session }

func (p *logonProc) Job() runtime.Job {
	if !p.killJob || p.fakeProc.job == nil {
		return p.fakeProc.job
	}
	return killingJob{Job: p.fakeProc.job, main: p.fakeProc}
}

// killingJob ends the fake main process when the unit job is killed, as
// TerminateJobObject does.
type killingJob struct {
	runtime.Job
	main *fakeProc
}

func (j killingJob) Kill() error {
	j.main.die(1)
	return j.Job.Kill()
}

type signedInSessions struct {
	mu   sync.Mutex
	gone map[uint32]int // remaining answers "signed out" per session
}

func (s *signedInSessions) signedIn(session uint32, sid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sid != aliceSID {
		return false
	}
	if s.gone[session] > 0 {
		s.gone[session]--
		return false
	}
	return true
}

const sessionService = userService + "User=EXAMPLE\\alice\nUserLogon=session-or-s4u\n"

func sessionUnitManager(t *testing.T, body string, launch *sessionLauncher, sessions *signedInSessions) *Manager {
	t.Helper()
	base := t.TempDir()
	units := filepath.Join(base, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "work.service", body)
	if launch.mode == "" {
		launch.mode = "cooperate"
	}
	if sessions == nil {
		sessions = &signedInSessions{}
	}
	dir := &accountDirectory{}
	m, err := New(Config{BaseDir: base, Launch: launch, LookupAccount: dir.lookup, SessionSignedIn: sessions.signedIn, NotifyListen: testNotifyListen(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m
}

func unitStatus(t *testing.T, m *Manager) *protocol.UnitStatus {
	t.Helper()
	st, err := m.Status("work")
	if err != nil {
		t.Fatal(err)
	}
	return st.Unit
}

func journalText(t *testing.T, m *Manager) string {
	t.Helper()
	logs, err := m.Logs(protocol.LogsParams{Unit: "work"})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range logs.Entries {
		lines = append(lines, e.Message)
	}
	return strings.Join(lines, "\n")
}

func TestUserLogonStatusAndStopHelper(t *testing.T) {
	for _, tc := range []struct {
		logon       string
		session     uint32
		statusLine  string
		helperFirst bool
	}{
		{runtime.LogonSession, 2, "logon: session 2", true},
		{runtime.LogonS4U, 0, "logon: s4u", false},
	} {
		t.Run(tc.logon, func(t *testing.T) {
			launch := &sessionLauncher{logon: tc.logon, session: tc.session}
			m := sessionUnitManager(t, sessionService, launch, nil)
			if _, err := m.Start(context.Background(), "work"); err != nil {
				t.Fatal(err)
			}
			if w := launch.workloads(); len(w) != 1 || !w[0].UserSession {
				t.Fatalf("workload launch %+v", w)
			}
			st := unitStatus(t, m)
			if st.UserLogon != tc.logon || st.UserSession != tc.session || st.UserSID != aliceSID {
				t.Fatalf("running status %+v", st)
			}
			waitCond(t, func() bool { return strings.Contains(journalText(t, m), tc.statusLine) })
			if _, err := m.Stop("work"); err != nil {
				t.Fatal(err)
			}
			// The stop helper asks for the session logon only for an invocation
			// on one, whose session is still signed in.
			if h := launch.helpers(); len(h) != 1 || h[0].UserSession != tc.helperFirst || h[0].User != `EXAMPLE\alice` {
				t.Fatalf("stop helper %+v", h)
			}
			if st := unitStatus(t, m); st.UserLogon != "" || st.UserSession != 0 {
				t.Fatalf("stopped status %+v", st)
			}
		})
	}
}

func TestUserLogonDefaultAsksForS4U(t *testing.T) {
	launch := &sessionLauncher{logon: runtime.LogonS4U}
	m := sessionUnitManager(t, userService+"User=EXAMPLE\\alice\n", launch, nil)
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	if w := launch.workloads(); len(w) != 1 || w[0].UserSession {
		t.Fatalf("workload launch %+v", w)
	}
	if st := unitStatus(t, m); st.UserLogon != runtime.LogonS4U || st.UserSession != 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestUserLogonSignOutEndsTheInvocation(t *testing.T) {
	reason := "session 2, whose logon this invocation used, signed out"
	t.Run("on-failure restarts once", func(t *testing.T) {
		launch := &sessionLauncher{logon: runtime.LogonSession, session: 2}
		m := sessionUnitManager(t, sessionService+"Restart=on-failure\nRestartSec=200ms\n", launch, nil)
		if _, err := m.Start(context.Background(), "work"); err != nil {
			t.Fatal(err)
		}
		first := unitStatus(t, m).InvocationID
		// SCM and the poll usually both report one logoff.
		m.SessionSignedOut(2)
		m.SessionSignedOut(2)
		waitCond(t, func() bool {
			st := unitStatus(t, m)
			return len(launch.workloads()) == 2 && st.ActiveState == "active" && st.InvocationID != first
		})
		if h := launch.helpers(); len(h) != 1 || h[0].UserSession {
			t.Fatalf("stop helpers %+v, want one by S4U", h)
		}
		if !strings.Contains(journalText(t, m), reason) {
			t.Fatalf("journal lacks the sign-out reason:\n%s", journalText(t, m))
		}
	})
	t.Run("no restart stays failed", func(t *testing.T) {
		launch := &sessionLauncher{logon: runtime.LogonSession, session: 2}
		m := sessionUnitManager(t, sessionService+"Restart=no\n", launch, nil)
		if _, err := m.Start(context.Background(), "work"); err != nil {
			t.Fatal(err)
		}
		m.SessionSignedOut(2)
		waitCond(t, func() bool {
			st := unitStatus(t, m)
			return st.ActiveState == "failed" && st.Error == reason && st.MainPID == 0
		})
		if len(launch.workloads()) != 1 || len(launch.helpers()) != 1 {
			t.Fatalf("launches %+v", launch.specs())
		}
	})
	t.Run("operator stop wins", func(t *testing.T) {
		launch := &sessionLauncher{logon: runtime.LogonSession, session: 2}
		m := sessionUnitManager(t, sessionService+"Restart=always\nRestartSec=20ms\n", launch, nil)
		if _, err := m.Start(context.Background(), "work"); err != nil {
			t.Fatal(err)
		}
		m.SessionSignedOut(2)
		if _, err := m.Stop("work"); err != nil {
			t.Fatal(err)
		}
		waitCond(t, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.units["work.service"].operations == 0
		})
		time.Sleep(100 * time.Millisecond) // longer than RestartSec
		if st := unitStatus(t, m); st.ActiveState != "inactive" || len(launch.workloads()) != 1 {
			t.Fatalf("after stop %+v, launches %d", st, len(launch.workloads()))
		}
	})
	t.Run("other logoffs do nothing", func(t *testing.T) {
		for _, logon := range []string{runtime.LogonSession, runtime.LogonS4U} {
			launch := &sessionLauncher{logon: logon, session: 2}
			if logon == runtime.LogonS4U {
				launch.session = 0
			}
			m := sessionUnitManager(t, sessionService+"Restart=always\n", launch, nil)
			if _, err := m.Start(context.Background(), "work"); err != nil {
				t.Fatal(err)
			}
			first := unitStatus(t, m).InvocationID
			m.SessionSignedOut(3)
			if logon == runtime.LogonS4U {
				m.SessionSignedOut(2)
				m.SessionSignedOut(0)
			}
			m.mu.Lock()
			ended := m.units["work.service"].sessionEnded
			m.mu.Unlock()
			st := unitStatus(t, m)
			if ended || st.InvocationID != first || st.ActiveState != "active" || len(launch.workloads()) != 1 {
				t.Fatalf("%s invocation changed: ended=%t %+v", logon, ended, st)
			}
		}
	})
}

func TestUserLogonSignOutDuringLaunch(t *testing.T) {
	reason := "session 2, whose logon this invocation used, signed out"
	held := func(launch *sessionLauncher) {
		launch.hold, launch.entered = make(chan struct{}), make(chan struct{})
	}
	// startDuringLogoff runs the start, delivers the logoff while the launcher
	// is inside Start and returns the start's error.
	startDuringLogoff := func(t *testing.T, m *Manager, launch *sessionLauncher) error {
		t.Helper()
		hold, entered := launch.hold, launch.entered
		result := make(chan error, 1)
		go func() {
			_, err := m.Start(context.Background(), "work")
			result <- err
		}()
		<-entered
		m.SessionSignedOut(2)
		close(hold)
		return <-result
	}
	t.Run("simple", func(t *testing.T) {
		launch := &sessionLauncher{logon: runtime.LogonSession, session: 2, killJob: true}
		held(launch)
		m := sessionUnitManager(t, sessionService+"Restart=on-failure\nRestartSec=20ms\n", launch, nil)
		if err := startDuringLogoff(t, m, launch); err == nil || !strings.Contains(err.Error(), reason) {
			t.Fatalf("start = %v, want the sign-out reason", err)
		}
		waitCond(t, func() bool { return len(launch.workloads()) == 2 && unitStatus(t, m).ActiveState == "active" })
	})
	t.Run("readiness wins the race", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
		defer server.Close()
		// The killed process still looks alive and ready: activation is refused.
		launch := &sessionLauncher{logon: runtime.LogonSession, session: 2}
		held(launch)
		body := sessionService + fmt.Sprintf("ReadinessMode=http\nReadinessEndpoint=%s\nReadinessIntervalSec=50ms\nReadinessTimeoutSec=1s\nTimeoutStartSec=5s\nRestart=on-failure\nRestartSec=20ms\n", server.URL)
		m := sessionUnitManager(t, body, launch, nil)
		if err := startDuringLogoff(t, m, launch); err == nil || !strings.Contains(err.Error(), reason) {
			t.Fatalf("start = %v, want the sign-out reason", err)
		}
		waitCond(t, func() bool { return len(launch.workloads()) == 2 && unitStatus(t, m).ActiveState == "active" })
	})
	t.Run("launch error after sign-out", func(t *testing.T) {
		launch := &sessionLauncher{logon: runtime.LogonSession, session: 2}
		launch.startErr = &runtime.AccountError{Account: aliceSID, Err: errors.New("session 2: signed out during the launch: CreateProcessAsUser: access denied")}
		m := sessionUnitManager(t, sessionService+"Restart=on-failure\nRestartSec=20ms\n", launch, nil)
		if _, err := m.Start(context.Background(), "work"); err == nil || !strings.Contains(err.Error(), "signed out during the launch") {
			t.Fatalf("start = %v", err)
		}
		waitCond(t, func() bool { return len(launch.workloads()) == 2 && unitStatus(t, m).ActiveState == "active" })
	})
	t.Run("gone before the launch registered", func(t *testing.T) {
		// The logoff went to nobody; the check at adoption finds it.
		launch := &sessionLauncher{logon: runtime.LogonSession, session: 2, killJob: true}
		sessions := &signedInSessions{gone: map[uint32]int{2: 1}}
		m := sessionUnitManager(t, sessionService+"Restart=on-failure\nRestartSec=20ms\n", launch, sessions)
		if _, err := m.Start(context.Background(), "work"); err == nil || !strings.Contains(err.Error(), reason) {
			t.Fatalf("start = %v, want the sign-out reason", err)
		}
		waitCond(t, func() bool { return len(launch.workloads()) == 2 && unitStatus(t, m).ActiveState == "active" })
	})
}

func TestUserHostReportsLogoffsToTheSystemManager(t *testing.T) {
	var mu sync.Mutex
	var got []uint32
	h := NewUserHost(UserHostConfig{
		QueryToken:      func(uint32) (*runtime.UserToken, error) { return nil, errors.New("no token in this test") },
		Sessions:        func() ([]uint32, error) { return nil, nil },
		OnSessionLogoff: func(id uint32) { mu.Lock(); got = append(got, id); mu.Unlock() },
	})
	ch := make(chan runtime.SessionChange, 4)
	// Lock and disconnect never become a SessionChange (ParseSessionChange);
	// a logon and a logoff do.
	ch <- runtime.SessionChange{SessionID: 1, Logon: true}
	ch <- runtime.SessionChange{SessionID: 2, Logon: false}
	close(ch)
	h.Listen(context.Background(), ch)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("logoff hook calls %v, want [2]", got)
	}
}
