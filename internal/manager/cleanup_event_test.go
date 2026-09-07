package manager

import (
	"context"
	"errors"
	"testing"

	"github.com/PLN/winunitd/internal/core"
)

func TestStaleCleanupEventCannotChangeReplacement(t *testing.T) {
	for _, source := range []string{"watchdog", "exit", "failed-process", "readiness", "oneshot", "late-launch", "activation"} {
		for _, failed := range []bool{false, true} {
			label := source + "/success"
			if failed {
				label = source + "/failure"
			}
			t.Run(label, func(t *testing.T) {
				const name = "work.service"
				m := managerWith(t, &fakeLauncher{}, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
				if _, err := m.Start(context.Background(), name); err != nil {
					t.Fatal(err)
				}
				m.mu.Lock()
				rt := m.units[name]
				owner, proc := runtimeIdentity{name: name, record: rt, gen: rt.gen}, rt.proc
				capturedUnit := rt.ownedUnit()
				m.mu.Unlock()
				// Capture the accepted cleanup instruction, then model a delayed
				// result arriving after explicit stop has already resolved it.
				var deliver func(error) bool
				if source == "watchdog" {
					effect := m.acceptWatchdogFailure(owner)
					if effect == nil {
						t.Fatal("watchdog cleanup was not accepted")
					}
					deliver = func(err error) bool { return m.applyWatchdogCleanup(watchdogCleanup{effect: effect, err: err}) }
				} else if source == "exit" {
					effect := m.acceptProcessExitCleanup(name, proc)
					if effect == nil {
						t.Fatal("exit cleanup was not accepted")
					}
					deliver = func(err error) bool { return m.applyProcessExitCleanup(processExitCleanup{effect: effect, err: err}) }
				} else if source == "failed-process" {
					effect := failedProcessEffect{owner: owner, process: proc}
					deliver = func(err error) bool { m.applyFailedProcessCleanup(effect, err); return false }
				} else {
					effect := &launchEffect{owner: owner, unit: capturedUnit}
					deliver = func(err error) bool {
						switch source {
						case "readiness":
							m.applyReadinessCleanup(effect, proc, err)
						case "oneshot":
							m.applyOneshotCleanup(effect, proc, err, true)
						case "late-launch":
							m.applyLateLaunchCleanup(effect, proc, err)
						case "activation":
							_, accepted := m.acceptProcessActivation(context.Background(), effect, proc)
							return accepted
						}
						return false
					}
				}
				if _, err := m.Stop(name); err != nil {
					t.Fatal(err)
				}
				if _, err := m.Start(context.Background(), name); err != nil {
					t.Fatal(err)
				}
				m.mu.Lock()
				replacement := rt.proc
				m.mu.Unlock()
				var lateErr error
				if failed {
					lateErr = errors.New("old cleanup failed")
				}
				if deliver(lateErr) {
					t.Fatal("stale cleanup authorized recovery")
				}
				m.mu.Lock()
				unchanged := rt.proc == replacement && rt.state == core.Active && rt.err == "" && !rt.stopUncertain
				m.mu.Unlock()
				if !unchanged || !replacement.Alive() {
					t.Fatal("stale cleanup changed replacement ownership or diagnostics")
				}
			})
		}
	}
}
