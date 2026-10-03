//go:build windows

package headless

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Helper processes for the observer test: a copy of this binary named like
// the daemon restarts a copy named like the workload whenever it exits.
const (
	helperRole    = "HEADLESS_OBSERVER_HELPER"
	helperWork    = "HEADLESS_OBSERVER_WORKLOAD"
	helperRelease = "HEADLESS_OBSERVER_RELEASE"
	helperPipe    = "HEADLESS_TEST_PIPE"
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperRole) {
	case "pipe-exit":
		// Sends its claim and exits without waiting for the verdict.
		ProbePipe(os.Getenv(helperPipe), ClientExited, "self", false, true)
		os.Exit(3)
	case "manager":
		for range 100 {
			cmd := exec.Command(os.Getenv(helperWork))
			cmd.Env = append(os.Environ(), helperRole+"=workload")
			_ = cmd.Run()
			time.Sleep(200 * time.Millisecond)
		}
		time.Sleep(time.Hour)
		os.Exit(0)
	case "workload":
		if release := os.Getenv(helperRelease); release != "" {
			if _, err := os.Stat(release); err != nil {
				time.Sleep(400 * time.Millisecond)
				os.Exit(7)
			}
		}
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type observerRig struct {
	dir, daemon, work, sid string
	manager                *exec.Cmd
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func newObserverRig(t *testing.T, release string) *observerRig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := &observerRig{dir: t.TempDir()}
	r.daemon, r.work = filepath.Join(r.dir, "winunitd.exe"), filepath.Join(r.dir, "headless-workload.exe")
	copyFile(t, exe, r.daemon)
	copyFile(t, exe, r.work)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	r.sid = user.User.Sid.String()
	r.manager = exec.Command(r.daemon)
	r.manager.Env = append(os.Environ(), helperRole+"=manager", helperWork+"="+r.work, helperRelease+"="+release)
	if err := r.manager.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.manager.Process.Kill(); _ = r.manager.Wait() })
	time.Sleep(time.Second)
	return r
}

// observe runs the observer in this process and returns its finished report.
func (r *observerRig) observe(t *testing.T, extra ...string) *ObserverReport {
	t.Helper()
	report := filepath.Join(r.dir, "observer.json")
	args := append([]string{"--report", report, "--admission", writeTestAdmission(t), "--daemon-image", r.daemon,
		"--workload-image", r.work, "--account", "A=" + r.sid}, extra...)
	if err := runObserve(args); err != nil {
		t.Fatal(err)
	}
	data, err := readBounded(report)
	if err != nil {
		t.Fatal(err)
	}
	var rep ObserverReport
	if err := decodeStrict(data, &rep); err != nil {
		t.Fatal(err)
	}
	if err := rep.Validate(); err != nil {
		t.Fatalf("report: %v", err)
	}
	t.Cleanup(func() {
		_ = r.manager.Process.Kill()
		_ = r.manager.Wait()
		for _, g := range rep.Generations {
			if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, g.PID); err == nil {
				_ = windows.TerminateProcess(h, 1)
				_ = windows.CloseHandle(h)
			}
		}
	})
	if len(rep.Unidentified) != 0 || rep.MaxGap*100 > uint64(MaxObservationGap) {
		t.Fatalf("coverage: unidentified %v, max gap %d", rep.Unidentified, rep.MaxGap)
	}
	return &rep
}

// The observer holds the manager and its workload from the first scan,
// terminates exactly the planned generation after its life, records the
// replacement the manager starts and timestamps a mark.
func TestObserverCrashesThePlannedGeneration(t *testing.T) {
	r := newObserverRig(t, "")
	marks := filepath.Join(r.dir, "marks")
	if err := os.Mkdir(marks, 0o700); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(4 * time.Second)
		_ = os.WriteFile(filepath.Join(marks, "quiet"), nil, 0o600)
	}()
	rep := r.observe(t, "--duration", "8s", "--target", "A", "--crash", RoleWorkload, "--crash-lives", "1s", "--marks", marks)
	managers, workloads := gensOf(rep, RoleManager, AccountA), gensOf(rep, RoleWorkload, AccountA)
	if len(managers) != 1 || managers[0].Seen != rep.Started || managers[0].Exited != 0 || managers[0].PID != uint32(r.manager.Process.Pid) {
		t.Fatalf("manager %+v", managers)
	}
	if len(workloads) != 2 {
		t.Fatalf("workloads %+v", workloads)
	}
	old, repl := workloads[0], workloads[1]
	if old.Seen != rep.Started || old.Crashed == 0 || old.ExitCode != crashExitCode || old.Crashed-rep.Started < 1e7 ||
		repl.Created <= old.Exited || repl.Exited != 0 || repl.ParentPID != managers[0].PID {
		t.Fatalf("crash and replacement: %+v %+v", old, repl)
	}
	spec := ObserveSpec{Crash: RoleWorkload, Old: []string{RoleWorkload, RoleChild}, New: RoleWorkload}
	l, _ := DeriveLifecycle(rep, spec, AccountA, r.sid, ModeS4U)
	if metrics(l, "", 0)["orderedReplacement"] != 1 {
		t.Fatalf("replacement %+v", l.Replacement)
	}
	if len(rep.Marks) != 1 || rep.Marks[0].Name != "quiet" || rep.Marks[0].At < rep.Started+3e7 {
		t.Fatalf("marks %+v", rep.Marks)
	}
}

// Failing workloads are held for their whole short life with their exit
// code; after the planned failures the observer creates the release file
// and the next workload keeps running.
func TestObserverReleasesAfterFailures(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	r := newObserverRig(t, release)
	rep := r.observe(t, "--duration", "10s", "--target", "A", "--release-file", release, "--release-after", "3")
	workloads := gensOf(rep, RoleWorkload, AccountA)
	failed := 0
	for _, g := range workloads {
		if g.Exited != 0 && g.ExitCode == 7 {
			failed++
			if g.Exited-g.Created < 3e6 {
				t.Errorf("a failing workload lived %d", g.Exited-g.Created)
			}
		}
	}
	if failed < 3 || len(rep.Releases) != 1 || workloads[len(workloads)-1].Exited != 0 {
		t.Fatalf("failures %d, releases %+v, last %+v", failed, rep.Releases, workloads[len(workloads)-1])
	}
	if _, err := os.Stat(release); err != nil {
		t.Fatal(err)
	}
}
