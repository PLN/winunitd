package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/runtime/runtimetest"
	"github.com/PLN/winunitd/internal/servicing"
	"github.com/PLN/winunitd/internal/servicing/servicingtest"
)

func floorBase(t *testing.T) string {
	t.Helper()
	return servicingtest.DataRoot(t)
}

// stoppedManager stands for a verified stopped system manager.
func stoppedManager() error { return nil }

func runFloorArgs(build servicing.Build, args ...string) (int, string, string) {
	return runFloorEnv(floorEnv{build: build, stopped: stoppedManager}, args...)
}

func runFloorEnv(env floorEnv, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := runFloor(args, &out, &errb, env)
	return code, out.String(), errb.String()
}

func TestFloorVerbs(t *testing.T) {
	base := floorBase(t)
	clean := false
	build := servicing.Build{Version: "0.2.0", Commit: "abc", Modified: &clean, Features: []string{"exec-stop"}}
	if code, out, _ := runFloorArgs(build, "show", "--base-dir", base); code != 0 || !strings.Contains(out, "no compatibility floor") {
		t.Fatalf("show without floor: %d %q", code, out)
	}
	if code, out, _ := runFloorArgs(build, "check", "--base-dir", base); code != 0 || !strings.Contains(out, "admission allowed") {
		t.Fatalf("check without floor: %d %q", code, out)
	}
	code, out, errOut := runFloorArgs(build, "set", "--base-dir", base, "--min-version", "0.2.0", "--require", "exec-stop", "--require-clean")
	if code != 0 || !strings.Contains(out, "floor raised") {
		t.Fatalf("set: %d %q %q", code, out, errOut)
	}
	if code, out, _ := runFloorArgs(build, "show", "--base-dir", base); code != 0 || !strings.Contains(out, `"minVersion": "0.2.0"`) || !strings.Contains(out, `"exec-stop"`) {
		t.Fatalf("show: %d %q", code, out)
	}
	older := servicing.Build{Version: "0.1.0-alpha"}
	if code, out, _ := runFloorArgs(older, "check", "--base-dir", base); code != 1 || !strings.Contains(out, "below compatibility floor: version 0.1.0-alpha is below 0.2.0") {
		t.Fatalf("check below floor: %d %q", code, out)
	}
	// A floor that the running build would not satisfy is refused and the
	// existing floor is left in place.
	code, _, errOut = runFloorArgs(build, "set", "--base-dir", base, "--min-version", "0.3.0", "--require", "linger-s4u")
	if code != 1 || !strings.Contains(errOut, "version 0.2.0 is below 0.3.0; missing features linger-s4u") {
		t.Fatalf("unsatisfied set: %d %q", code, errOut)
	}
	if f, err := servicing.ReadFloor(servicing.FloorPath(base)); err != nil || f.MinVersion != "0.2.0" {
		t.Fatalf("refused set changed the floor: %+v %v", f, err)
	}
	if code, out, _ := runFloorArgs(build, "clear", "--base-dir", base); code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("clear: %d %q", code, out)
	}
	if code, _, _ := runFloorArgs(older, "check", "--base-dir", base); code != 0 {
		t.Fatal("cleared floor still holds")
	}
}

func TestFloorUsageAndUntrustedRecord(t *testing.T) {
	base := floorBase(t)
	build := servicing.Build{Version: "0.2.0"}
	for _, args := range [][]string{
		nil, {"raise"}, {"show", "extra"}, {"set", "--base-dir", base}, {"set", "--base-dir", base, "--min-version", "two"},
		{"check", "--min-version", "0.2.0"}, {"set", "--base-dir", base, "--require", "Exec Stop"},
	} {
		if code, _, _ := runFloorArgs(build, args...); code != 2 {
			t.Errorf("%q exit %d, want 2", args, code)
		}
	}
	// A record that names its version twice is unusable, not the lower one.
	if err := os.WriteFile(servicing.FloorPath(base), []byte(`{"schema":1,"minVersion":"9.0.0","minVersion":"0.1.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runFloorArgs(build, "check", "--base-dir", base); code != 1 || !strings.Contains(out, "unusable") {
		t.Fatalf("ambiguous record: %d %q", code, out)
	}
	if code, _, _ := runFloorArgs(build, "show", "--base-dir", base); code != 1 {
		t.Fatal("show trusted an ambiguous record")
	}
}

func TestRunRoutesFloorVerbs(t *testing.T) {
	base := floorBase(t)
	var out, errb bytes.Buffer
	if code := run([]string{"floor", "check", "--base-dir", base}, &out, &errb); code != 0 || !strings.Contains(out.String(), "admission allowed") {
		t.Fatalf("run floor check: %d %q %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"floor", "--help"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "compat-floor.json") {
		t.Fatalf("floor help exit %d: %q", code, out.String())
	}
}

func TestStartupHoldFollowsTheFloor(t *testing.T) {
	base := floorBase(t)
	build := servicing.Build{Version: "0.1.0-alpha"}
	if hold := startupHold(base, build); hold != "" {
		t.Fatalf("no floor: %q", hold)
	}
	if err := servicing.WriteFloor(servicing.FloorPath(base), &servicing.Floor{Schema: 1, MinVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if hold := startupHold(base, build); !strings.HasPrefix(hold, "below compatibility floor: ") {
		t.Fatalf("hold = %q", hold)
	}
}

func TestStartAdmittedWorkRespectsTheHold(t *testing.T) {
	for _, hold := range []string{"", "below compatibility floor: version 0.1.0-alpha is below 0.2.0"} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "units"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "units", "work.service"), []byte("[Service]\nExecStart=C:\\Tools\\work.exe\n[Install]\nWantedBy=default.target\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		m, err := manager.New(manager.Config{BaseDir: dir, Launch: runtimetest.Launcher(), AdmissionHold: hold})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Reload(); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Enable("work.service"); err != nil {
			t.Fatal(err)
		}
		var sessions atomic.Int32
		host := manager.NewUserHost(manager.UserHostConfig{
			AdmissionHold: hold,
			Sessions:      func() ([]uint32, error) { sessions.Add(1); return nil, nil },
		})
		var logged []string
		done := startAdmittedWork(context.Background(), m, host, hold, nil, func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) })
		if done != nil {
			<-done
		}
		st, err := m.Status("work.service")
		if err != nil {
			t.Fatal(err)
		}
		if hold == "" {
			if st.Unit.ActiveState != "active" || sessions.Load() != 1 {
				t.Fatalf("unheld start: state %s, reconciliations %d", st.Unit.ActiveState, sessions.Load())
			}
		} else if done != nil || st.Unit.ActiveState == "active" || sessions.Load() != 0 || len(logged) != 1 {
			t.Fatalf("held start: state %s, reconciliations %d, log %q", st.Unit.ActiveState, sessions.Load(), logged)
		}
		if err := finishContext(context.Background(), m, nil, host, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
}

// A raise needs the system manager stopped, verified before and after the
// write; lowering, rewriting the same floor and clearing do not.
func TestFloorRaiseNeedsAStoppedManager(t *testing.T) {
	base := floorBase(t)
	path := servicing.FloorPath(base)
	clean := false
	build := servicing.Build{Version: "0.3.0", Commit: "abc", Modified: &clean}
	running := floorEnv{build: build, stopped: func() error { return errors.New("the winunitd service is not stopped") }}
	read := func() *servicing.Floor {
		t.Helper()
		f, err := servicing.ReadFloor(path)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	code, _, errOut := runFloorEnv(running, "set", "--base-dir", base, "--min-version", "0.2.0")
	if code != 1 || !strings.Contains(errOut, "refused: raising the floor needs the system manager stopped: the winunitd service is not stopped") {
		t.Fatalf("first floor while running: %d %q", code, errOut)
	}
	if f := read(); f != nil {
		t.Fatalf("refused raise wrote %+v", f)
	}
	if code, out, errOut := runFloorArgs(build, "set", "--base-dir", base, "--min-version", "0.2.0", "--require-clean"); code != 0 || !strings.Contains(out, "floor raised") {
		t.Fatalf("raise while stopped: %d %q %q", code, out, errOut)
	}
	for _, args := range [][]string{
		{"--min-version", "0.3.0"},
		{"--min-version", "0.2.0", "--require-clean", "--require", "exec-stop"},
		{"--min-version", "0.2.0"},
	} {
		code, _, errOut := runFloorEnv(running, append([]string{"set", "--base-dir", base}, args...)...)
		if strings.Contains(strings.Join(args, " "), "exec-stop") {
			// Not satisfiable by this build: refused before any stop check.
			if code != 1 || !strings.Contains(errOut, "does not satisfy it") {
				t.Fatalf("%q: %d %q", args, code, errOut)
			}
			continue
		}
		wantRaise := args[1] == "0.3.0"
		if wantRaise && (code != 1 || !strings.Contains(errOut, "needs the system manager stopped")) {
			t.Fatalf("raise %q while running: %d %q", args, code, errOut)
		}
		if !wantRaise && code != 0 {
			t.Fatalf("lowering %q while running: %d %q", args, code, errOut)
		}
	}
	if f := read(); f.MinVersion != "0.2.0" || f.RequireCleanBuild {
		t.Fatalf("floor after lowering %+v", f)
	}
	if code, out, _ := runFloorEnv(running, "set", "--base-dir", base, "--min-version", "0.1.0"); code != 0 || !strings.Contains(out, "without raising") {
		t.Fatalf("lower while running: %d %q", code, out)
	}
	if code, _, _ := runFloorEnv(running, "set", "--base-dir", base, "--require-clean"); code != 1 {
		t.Fatal("adding the clean-build requirement while running was not refused")
	}
	if code, _, _ := runFloorEnv(running, "clear", "--base-dir", base); code != 0 {
		t.Fatal("clear while running was refused")
	}

	// A manager that starts during the change is reported; the raised
	// floor stays and that manager must be restarted.
	calls := 0
	racing := floorEnv{build: build, stopped: func() error {
		calls++
		if calls > 1 {
			return errors.New("the winunitd service is not stopped")
		}
		return nil
	}}
	code, _, errOut = runFloorEnv(racing, "set", "--base-dir", base, "--min-version", "0.2.0")
	if code != 1 || calls != 2 || !strings.Contains(errOut, "a manager started during the change") {
		t.Fatalf("racing start: %d calls %d %q", code, calls, errOut)
	}
	if f := read(); f == nil || f.MinVersion != "0.2.0" {
		t.Fatalf("racing start left %+v", f)
	}

	// A failed write leaves the floor as it was.
	missing := servicingtest.Root(t)
	if code, _, errOut := runFloorArgs(build, "set", "--base-dir", missing, "--min-version", "0.2.0"); code != 1 || !strings.Contains(errOut, "does not exist") {
		t.Fatalf("write without a daemon directory: %d %q", code, errOut)
	}
	if f, err := servicing.ReadFloor(servicing.FloorPath(missing)); err != nil || f != nil {
		t.Fatalf("failed write left %+v %v", f, err)
	}
}

// The order a workload that needs a raised floor relies on: a running
// manager that loaded an older build keeps admitting under the floor it
// started with, so the raise is refused until it stops. After the raise,
// a restart of that older build holds every start and broker launch, and
// the newer build on disk admits.
func TestFloorRaiseOrderWithAnActiveManager(t *testing.T) {
	base := floorBase(t)
	units := filepath.Join(base, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"work.service", "extra.service"} {
		body := "[Service]\nExecStart=C:\\Tools\\" + name + ".exe\n[Install]\nWantedBy=default.target\n"
		if err := os.WriteFile(filepath.Join(units, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	loaded := servicing.Build{Version: "0.1.0"}
	onDisk := servicing.Build{Version: "0.2.0"}
	start := func(build servicing.Build) (*manager.Manager, *manager.UserHost, *atomic.Int32, string) {
		t.Helper()
		hold := startupHold(base, build)
		m, err := manager.New(manager.Config{BaseDir: base, Launch: runtimetest.Launcher(), AdmissionHold: hold})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Reload(); err != nil {
			t.Fatal(err)
		}
		var sessions atomic.Int32
		host := manager.NewUserHost(manager.UserHostConfig{
			AdmissionHold: hold,
			Sessions:      func() ([]uint32, error) { sessions.Add(1); return nil, nil },
		})
		return m, host, &sessions, hold
	}
	stop := func(m *manager.Manager, host *manager.UserHost) {
		t.Helper()
		if err := finishContext(context.Background(), m, nil, host, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	active := func(m *manager.Manager, name string) bool {
		t.Helper()
		st, err := m.Status(name)
		if err != nil {
			t.Fatal(err)
		}
		return st.Unit.ActiveState == "active"
	}

	old, oldHost, _, hold := start(loaded)
	if hold != "" {
		t.Fatalf("no floor held the loaded build: %s", hold)
	}
	if _, err := old.Enable("work.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Start(context.Background(), "work.service"); err != nil {
		t.Fatal(err)
	}
	running := true
	env := floorEnv{build: onDisk, stopped: func() error {
		if running {
			return errors.New("the winunitd service is not stopped")
		}
		return nil
	}}
	if code, _, errOut := runFloorEnv(env, "set", "--base-dir", base, "--min-version", "0.2.0"); code != 1 || !strings.Contains(errOut, "needs the system manager stopped") {
		t.Fatalf("raise with an active manager: %d %q", code, errOut)
	}
	// The floor is unchanged, so the running manager's admission is still
	// the one its floor allows.
	if _, err := old.Start(context.Background(), "extra.service"); err != nil {
		t.Fatal(err)
	}
	stop(old, oldHost)
	running = false
	if code, _, errOut := runFloorEnv(env, "set", "--base-dir", base, "--min-version", "0.2.0"); code != 0 {
		t.Fatalf("raise after stop: %d %q", code, errOut)
	}

	// The older build restarts held: no explicit start, boot or broker
	// launch.
	held, heldHost, sessions, hold := start(loaded)
	if !strings.HasPrefix(hold, "below compatibility floor: version 0.1.0 is below 0.2.0") {
		t.Fatalf("older build hold %q", hold)
	}
	if _, err := held.Start(context.Background(), "work.service"); err == nil || !strings.Contains(err.Error(), "admission is held") {
		t.Fatalf("held start: %v", err)
	}
	if done := startAdmittedWork(context.Background(), held, heldHost, hold, nil, func(string, ...any) {}); done != nil {
		t.Fatal("held manager started reconciliation")
	}
	if active(held, "work.service") || active(held, "extra.service") || sessions.Load() != 0 {
		t.Fatalf("held manager admitted work: reconciliations %d", sessions.Load())
	}
	stop(held, heldHost)

	// The build on disk, which the writer evaluated, admits.
	fresh, freshHost, sessions, hold := start(onDisk)
	if hold != "" {
		t.Fatalf("on-disk build held: %s", hold)
	}
	if done := startAdmittedWork(context.Background(), fresh, freshHost, hold, nil, func(string, ...any) {}); done != nil {
		<-done
	}
	// Enabled units return; the manually started one does not.
	if !active(fresh, "work.service") || active(fresh, "extra.service") || sessions.Load() != 1 {
		t.Fatalf("on-disk build did not admit as enabled: reconciliations %d", sessions.Load())
	}
	stop(fresh, freshHost)
}
