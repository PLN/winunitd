//go:build windows

package headless

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Helper processes for the observer test: a copy of this binary named like
// the daemon waits for a go file, then restarts a copy named like the
// workload whenever it exits. The test holds them all in a kill-on-close
// job of its own.
const (
	helperRole    = "HEADLESS_OBSERVER_HELPER"
	helperWork    = "HEADLESS_OBSERVER_WORKLOAD"
	helperRelease = "HEADLESS_OBSERVER_RELEASE"
	helperGo      = "HEADLESS_OBSERVER_GO"
	helperPipe    = "HEADLESS_TEST_PIPE"
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperRole) {
	case "pipe-exit":
		// Sends its claim and exits without waiting for the verdict.
		ProbePipe(os.Getenv(helperPipe), ClientExited, "self", false, true)
		os.Exit(3)
	case "manager":
		// Start nothing until the test has put this process in its job.
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(os.Getenv(helperGo)); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(4)
			}
			time.Sleep(20 * time.Millisecond)
		}
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
	dir, daemon, work, sid, admission string
	manager                           *exec.Cmd
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

// workloadRunning reports whether a workload copy started by the manager is
// running.
func workloadRunning(manager uint32) bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ParentProcessID == manager && strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), workloadImage) {
			return true
		}
	}
	return false
}

func newObserverRig(t *testing.T, release string) *observerRig {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		t.Skip("the observer rig watches an ordinary account; a SYSTEM runner has none to watch")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := &observerRig{dir: t.TempDir(), sid: user.User.Sid.String()}
	r.daemon, r.work = filepath.Join(r.dir, daemonImage), filepath.Join(r.dir, workloadImage)
	copyFile(t, exe, r.daemon)
	copyFile(t, exe, r.work)
	sum, err := FileSHA256(exe)
	if err != nil {
		t.Fatal(err)
	}
	// Both images are copies of this binary, admitted under their names.
	data, err := json.Marshal(Admission{Schema: AdmissionSchema, Source: testSource,
		Artifacts: []Artifact{{Name: workloadImage, SHA256: sum}, {Name: daemonImage, SHA256: sum}}})
	if err != nil {
		t.Fatal(err)
	}
	r.admission = filepath.Join(r.dir, "admission.json")
	if err := os.WriteFile(r.admission, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// The helper tree runs in a kill-on-close job from its first
	// instruction. One cleanup, registered before anything else can fail,
	// terminates the tree, then reaps the exact manager, then confirms the
	// job is empty and closes it, so no helper outlives the test and no PID
	// is reused for a kill.
	c, err := newCustody()
	if err != nil {
		t.Fatal(err)
	}
	start := filepath.Join(r.dir, "go")
	r.manager = exec.Command(r.daemon)
	r.manager.Env = append(os.Environ(), helperRole+"=manager", helperWork+"="+r.work, helperRelease+"="+release, helperGo+"="+start)
	if err := c.start(r.manager); err != nil {
		_ = c.end(15 * time.Second)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.terminate()
		_ = r.manager.Wait()
		if err := c.end(15 * time.Second); err != nil {
			t.Error(err)
		}
	})
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for !workloadRunning(uint32(r.manager.Process.Pid)) {
		if time.Now().After(deadline) {
			t.Fatal("the helper manager did not start its workload")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return r
}

// observe runs the observer in this process, outside qualification, and
// returns its finished report.
func (r *observerRig) observe(t *testing.T, extra ...string) *ObserverReport {
	t.Helper()
	report := filepath.Join(r.dir, "observer.json")
	args := append([]string{"--report", report, "--admission", r.admission, "--daemon-image", r.daemon,
		"--workload-image", r.work, "--account", "A=" + r.sid}, extra...)
	if err := observeWith(args, false); err != nil {
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
	if err := rep.validateStructure(); err != nil {
		t.Fatalf("report: %v", err)
	}
	// An ordinary account's report is never qualification evidence.
	if rep.Validate() == nil {
		t.Fatal("a non-SYSTEM observer's report validated as qualification evidence")
	}
	if len(rep.Unidentified) != 0 || rep.MaxGap*100 > uint64(MaxObservationGap) {
		t.Fatalf("coverage: %d unidentified, max gap %d", len(rep.Unidentified), rep.MaxGap)
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
		t.Fatalf("%d manager generations; the first held from the start, running and the helper: %t", len(managers),
			len(managers) > 0 && managers[0].Seen == rep.Started && managers[0].Exited == 0 && managers[0].PID == uint32(r.manager.Process.Pid))
	}
	if len(workloads) != 2 {
		t.Fatalf("%d workload generations, want 2", len(workloads))
	}
	old, repl := workloads[0], workloads[1]
	if old.Seen != rep.Started || old.Crashed == 0 || old.ExitCode != crashExitCode || old.Crashed-rep.Started < 1e7 ||
		repl.Created <= old.Exited || repl.Exited != 0 || repl.ParentPID != managers[0].PID {
		t.Fatalf("crash and replacement: crashed %t, exit code %#x, replacement after the exit %t, running %t, started by the manager %t",
			old.Crashed != 0, old.ExitCode, repl.Created > old.Exited, repl.Exited == 0, repl.ParentPID == managers[0].PID)
	}
	spec := ObserveSpec{Crash: RoleWorkload, Old: []string{RoleWorkload, RoleChild}, New: RoleWorkload}
	l, _ := DeriveLifecycle(rep, spec, AccountA, r.sid, ModeS4U)
	if metrics(l, nil, 0, 0)["orderedReplacement"] != 1 {
		t.Fatal("no ordered replacement derived")
	}
	if len(rep.Marks) != 1 || rep.Marks[0].Name != "quiet" || rep.Marks[0].At < rep.Started+3e7 {
		t.Fatalf("%d marks; the quiet mark timestamped after its creation: %t", len(rep.Marks), len(rep.Marks) == 1 && rep.Marks[0].At >= rep.Started+3e7)
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
				t.Errorf("a failing workload lived %d ms", (g.Exited-g.Created)/1e4)
			}
		}
	}
	if failed < 3 || len(rep.Releases) != 1 || workloads[len(workloads)-1].Exited != 0 {
		t.Fatalf("%d failures, %d releases, the last workload running %t", failed, len(rep.Releases), len(workloads) > 0 && workloads[len(workloads)-1].Exited == 0)
	}
	if _, err := os.Stat(release); err != nil {
		t.Fatal(err)
	}
}
