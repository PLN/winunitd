//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestPostCreationSetupFailuresRetainNativeOwnership(t *testing.T) {
	for _, stage := range []string{"outer-job", "unit-job", "io-priority", "resume-thread", "close-thread"} {
		for _, retain := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retain=%v", stage, retain), func(t *testing.T) {
				outer, err := OpenDaemonJob()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := outer.Close(); err != nil {
						t.Errorf("outer job cleanup: %v", err)
					}
				})
				injected := errors.New("injected " + stage + " failure")
				var setup processSetup
				called := false
				switch stage {
				case "outer-job":
					setup.assignDaemon = func(*DaemonJob, windows.Handle) error { called = true; return injected }
				case "unit-job":
					setup.assignUnit = func(*UnitJob, windows.Handle) error { called = true; return injected }
				case "io-priority":
					setup.priority = func(windows.Handle, uint32) error { called = true; return injected }
				case "resume-thread":
					setup.resume = func(windows.Handle) (uint32, error) { called = true; return 0, injected }
				case "close-thread":
					setup.closeThread = func(h windows.Handle) error {
						called = true
						if retain {
							if err := windows.SetHandleInformation(h, protectHandleFromClose, protectHandleFromClose); err != nil {
								return err
							}
							// A real native CloseHandle rejection after ResumeThread.
							return windows.CloseHandle(h)
						}
						return injected
					}
				}
				spec := StartSpec{Argv: []string{testAbs(t), winunitdHelperArgPrefix + "sleep"}, Env: helperEnv("WINUNITD_JOB_HELPER=sleep"), Limits: JobLimits{IoPrioritySet: true, IoPriority: 2}}
				p, cause := (&winLauncher{daemon: outer}).createWithSetup(spec, OpenUnitJobWith, makeStdPipe, openNUL, setup)
				if p == nil {
					t.Fatal("post-creation failure lost resource owner")
				}
				t.Cleanup(func() {
					for _, h := range []windows.Handle{p.thread, p.process} {
						if h != 0 {
							_ = windows.SetHandleInformation(h, protectHandleFromClose, 0)
						}
					}
					if err := p.Stop(2 * time.Second); err != nil {
						t.Errorf("fixture cleanup: %v", err)
					}
				})
				if !called || cause == nil || p.PID() == 0 || p.unassigned != (stage == "outer-job" || stage == "unit-job") {
					t.Fatalf("wrong setup boundary: called=%v error=%v pid=%d unassigned=%v", called, cause, p.PID(), p.unassigned)
				}
				probe, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p.PID()))
				if err != nil {
					t.Fatal(err)
				}
				defer windows.CloseHandle(probe)
				if retain {
					if err := windows.SetHandleInformation(p.process, protectHandleFromClose, protectHandleFromClose); err != nil {
						t.Fatal(err)
					}
				}
				owner, err := failedProcessStart(p, cause)
				if !errors.Is(err, cause) || (owner != nil) != retain || (retain && owner != p) {
					t.Fatalf("failure/cleanup ownership lost: owner=%v error=%v", owner != nil, err)
				}
				if state, err := windows.WaitForSingleObject(probe, 0); err != nil || state != windows.WAIT_OBJECT_0 {
					t.Fatalf("created process survived failed launch: state=%d error=%v", state, err)
				}
				if retain {
					if p.closed || p.process == 0 {
						t.Fatal("unfinished native close was reported released")
					}
					for _, h := range []windows.Handle{p.thread, p.process} {
						if h != 0 {
							if err := windows.SetHandleInformation(h, protectHandleFromClose, 0); err != nil {
								t.Fatal(err)
							}
						}
					}
					if err := owner.Stop(time.Second); err != nil {
						t.Fatal("native close retry", err)
					}
				}
				if !p.closed || p.process != 0 || p.thread != 0 || p.stdout != nil || p.stderr != nil || p.job.handle != 0 || p.launchHandles != [5]windows.Handle{} {
					t.Fatal("confirmed cleanup retained native resources")
				}
			})
		}
	}
}
