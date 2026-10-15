package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

// accountDirectory is a fake User= resolver: alice and bob resolve, once the
// configured failures, for every account or for bob, have run out, and
// nothing else does.
type accountDirectory struct {
	mu          sync.Mutex
	failures    int
	bobFailures int
	lookups     []string
}

func (d *accountDirectory) lookup(name string) (runtime.UserInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lookups = append(d.lookups, name)
	if d.failures > 0 || (d.bobFailures > 0 && strings.EqualFold(name, `EXAMPLE\bob`)) {
		if d.failures > 0 {
			d.failures--
		} else {
			d.bobFailures--
		}
		return runtime.UserInfo{}, errors.New("domain controller unreachable")
	}
	switch strings.ToLower(name) {
	case `example\alice`:
		return runtime.UserInfo{SID: "S-1-5-21-1-2-3-1001", Username: "alice", Domain: "EXAMPLE"}, nil
	case `example\bob`:
		return runtime.UserInfo{SID: "S-1-5-21-1-2-3-1002", Username: "bob", Domain: "EXAMPLE"}, nil
	}
	return runtime.UserInfo{}, errors.New("no such account")
}

func userUnitManager(t *testing.T, body string, dir *accountDirectory) (*Manager, *cooperativeLauncher, string) {
	t.Helper()
	base := t.TempDir()
	units := filepath.Join(base, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "work.service", body)
	launch := &cooperativeLauncher{mode: "cooperate"}
	m, err := New(Config{BaseDir: base, Launch: launch, LookupAccount: dir.lookup, NotifyListen: testNotifyListen(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m, launch, units
}

const userService = "[Unit]\nFormatVersion=2\n[Service]\nExecStart=C:\\Tools\\work.exe\nExecStop=C:\\Tools\\stop.exe\nWorkingDirectory=C:\\Tools\nEnvironment=CONFIG=unit\nTimeoutStopSec=5s\n"

func TestUserUnitRunsAsItsInvocationAccount(t *testing.T) {
	t.Setenv("WINUNITD_MANAGER_ONLY", "must-not-reach-the-unit")
	dir := &accountDirectory{}
	m, launch, units := userUnitManager(t, userService+"User=EXAMPLE\\alice\n", dir)
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	specs := launch.specs()
	if len(specs) != 1 || specs[0].User != "S-1-5-21-1-2-3-1001" {
		t.Fatalf("launch requests %+v", specs)
	}
	env := strings.Join(specs[0].Env, "\n")
	if !strings.Contains(env, "CONFIG=unit") || !strings.Contains(env, "WINUNIT_INVOCATION_ID=") || strings.Contains(env, "WINUNITD_MANAGER_ONLY") {
		t.Fatalf("User= environment carries the manager's or lacks the unit's: %q", env)
	}
	st, err := m.Status("work")
	if err != nil || st.Unit.User != `EXAMPLE\alice` || st.Unit.UserSID != "S-1-5-21-1-2-3-1001" {
		t.Fatalf("status %+v %v", st.Unit, err)
	}

	// A reload that changes the account leaves the running invocation alone:
	// its status and its stop command keep the account it started as.
	writeUnit(t, units, "work.service", userService+"User=EXAMPLE\\bob\n")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if st, _ := m.Status("work"); st.Unit.User != `EXAMPLE\alice` || st.Unit.UserSID != "S-1-5-21-1-2-3-1001" {
		t.Fatalf("status after reload %+v", st.Unit)
	}
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	specs = launch.specs()
	if len(specs) != 2 || specs[1].Argv[0] != `C:\Tools\stop.exe` || specs[1].User != `EXAMPLE\alice` {
		t.Fatalf("stop command launch %+v", specs)
	}
	if strings.Contains(strings.Join(specs[1].Env, "\n"), "WINUNITD_MANAGER_ONLY") {
		t.Fatal("stop command got the manager's environment")
	}
}

func TestUserUnitResolutionFailureRestarts(t *testing.T) {
	dir := &accountDirectory{failures: 1}
	m, launch, _ := userUnitManager(t, userService+"User=EXAMPLE\\alice\nRestart=on-failure\nRestartSec=20ms\n", dir)
	if _, err := m.Start(context.Background(), "work"); err == nil || !strings.Contains(err.Error(), `User=EXAMPLE\alice: domain controller unreachable`) {
		t.Fatalf("start with an unreachable domain: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(launch.specs()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no recovery start after the account resolved; lookups %v", dir.lookups)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if spec := launch.specs()[0]; spec.User != "S-1-5-21-1-2-3-1001" {
		t.Fatalf("recovery launch %+v", spec)
	}
}

func TestUserUnitWithSystemSpellingKeepsManagerEnvironment(t *testing.T) {
	t.Setenv("WINUNITD_MANAGER_ONLY", "kept")
	m, launch, _ := userUnitManager(t, userService+"User=SYSTEM\n", &accountDirectory{})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	spec := launch.specs()[0]
	if spec.User != "" || !strings.Contains(strings.Join(spec.Env, "\n"), "WINUNITD_MANAGER_ONLY=kept") {
		t.Fatalf("User=SYSTEM launch %+v", spec)
	}
	if st, _ := m.Status("work"); st.Unit.User != "" {
		t.Fatalf("User=SYSTEM status %+v", st.Unit)
	}
}

// A start of a reloaded definition that cannot resolve its account must be
// retried with that definition, not with the previous invocation's.
func TestUserUnitRecoveryUsesTheNewDefinition(t *testing.T) {
	dir := &accountDirectory{}
	m, launch, units := userUnitManager(t, userService+"User=EXAMPLE\\alice\n", dir)
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	// An invocation that exits by itself, unlike a stopped one, keeps its
	// definition as the one recovery would use.
	launch.guard.Lock()
	main := launch.main
	launch.guard.Unlock()
	main.die(0)
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		rt := m.units["work.service"]
		idle := rt.proc == nil && !rt.cleanupPending() && rt.invocationUnit != nil
		m.mu.Unlock()
		if idle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the main process exit was not cleaned up")
		}
		time.Sleep(10 * time.Millisecond)
	}
	writeUnit(t, units, "work.service", userService+"User=EXAMPLE\\bob\nRestart=on-failure\nRestartSec=20ms\n")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	dir.mu.Lock()
	dir.bobFailures = 1
	dir.mu.Unlock()
	if _, err := m.Start(context.Background(), "work"); err == nil {
		t.Fatal("start succeeded although the account did not resolve")
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		var workloads []runtime.StartSpec
		for _, spec := range launch.specs() {
			if spec.Argv[0] == `C:\Tools\work.exe` {
				workloads = append(workloads, spec)
			}
		}
		if len(workloads) >= 2 {
			if workloads[1].User != "S-1-5-21-1-2-3-1002" {
				t.Fatalf("recovery launched as %q, want bob", workloads[1].User)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no recovery start; lookups %v", dir.lookups)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Recovery discards a launch error, so an account failure in a recovery that
// Restart= does not repeat must still leave the unit failed with its error.
func TestUserUnitAccountFailureInRecoveryIsPublished(t *testing.T) {
	dir := &accountDirectory{}
	m, launch, _ := userUnitManager(t, userService+"User=EXAMPLE\\alice\nRestart=on-watchdog\n", dir)
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	launch.guard.Lock()
	main := launch.main
	launch.guard.Unlock()
	main.die(1)
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		rt := m.units["work.service"]
		idle := rt.proc == nil && !rt.cleanupPending()
		svc := rt.ownedUnit().Service
		m.mu.Unlock()
		if idle {
			dir.mu.Lock()
			dir.failures = 1
			dir.mu.Unlock()
			m.maybeRestart("work.service", core.ExitWatchdog, svc)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the main process exit was not cleaned up")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for {
		st, err := m.Status("work")
		if err == nil && st.Unit.ActiveState == "failed" && strings.Contains(st.Unit.Error, "domain controller unreachable") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after a failed recovery logon: %+v %v", st.Unit, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
