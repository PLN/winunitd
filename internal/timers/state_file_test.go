package timers

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var errStateFault = errors.New("injected timer file failure")

type failingStateFile struct {
	stateFile
	stage string
}

func (f failingStateFile) Write(p []byte) (int, error) {
	if f.stage == "short-write" {
		return f.stateFile.Write(p[:len(p)/2])
	}
	if f.stage == "write" {
		n, _ := f.stateFile.Write(p[:len(p)/2])
		return n, errStateFault
	}
	return f.stateFile.Write(p)
}
func (f failingStateFile) Sync() error {
	if f.stage == "sync" {
		return errStateFault
	}
	return f.stateFile.Sync()
}
func (f failingStateFile) Close() error {
	err := f.stateFile.Close()
	if f.stage == "close" {
		return errors.Join(err, errStateFault)
	}
	return err
}

func TestTimerStoreFileFailuresSuspendDispatch(t *testing.T) {
	for _, stage := range []string{"create", "write", "short-write", "sync", "close", "replace", "acknowledgement"} {
		t.Run(stage, func(t *testing.T) {
			store, spec, now := intentFixture(t)
			old := Runtime{LastActual: now.Add(-48 * time.Hour)}
			if err := store.Save(spec.Name, old); err != nil {
				t.Fatal(err)
			}
			ops := defaultStateFileOps()
			create, replace := ops.create, ops.replace
			ops.create = func(dir string) (stateFile, error) {
				if stage == "create" {
					return nil, errStateFault
				}
				f, err := create(dir)
				if err != nil {
					return nil, err
				}
				return failingStateFile{f, stage}, nil
			}
			ops.replace = func(from, to string) error {
				if stage == "replace" {
					return errStateFault
				}
				if err := replace(from, to); err != nil {
					return err
				}
				if stage == "acknowledgement" {
					return errStateFault
				}
				return nil
			}
			store.files = &ops
			fired := make(chan Fire, 2)
			e := NewEngine(NewFake(now).Clock(), store, func(f Fire) { fired <- f })
			e.Arm(spec)
			deadline := time.Now().Add(3 * time.Second)
			for e.Status(spec.Name).StorageState != "failed" && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			status := e.Status(spec.Name)
			e.Stop()
			if status.StorageState != "failed" || status.StorageError == "" || !status.Next.IsZero() {
				t.Fatalf("file failure did not suspend timer: %+v", status)
			}
			select {
			case <-fired:
				t.Fatal("dispatch without successful persistence")
			default:
			}
			disk, err := store.LoadChecked(spec.Name)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "acknowledgement" {
				if disk.Activation.Result != "pending" || disk.Activation.ID == "" {
					t.Fatal("completed replacement not recoverable")
				}
			} else if disk != old {
				t.Fatalf("failed replacement changed old state: %+v", disk)
			}
			files, err := filepath.Glob(filepath.Join(store.dir, ".timer-*"))
			if err != nil || len(files) != 0 {
				t.Fatal("failed write left temporary files")
			}
			// A fresh engine after repair must recover the old schedule or the uncertain
			// complete pending record, and then commit a result rather than losing work.
			store.files = nil
			var repaired *Engine
			repaired = NewEngine(NewFake(now).Clock(), store, func(f Fire) { repaired.RecordResult(f, true); fired <- f })
			defer repaired.Stop()
			repaired.Arm(spec)
			select {
			case f := <-fired:
				if stage == "acknowledgement" && f.ActivationID != disk.Activation.ID {
					t.Fatal("uncertain commit changed activation identity")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("repaired storage did not recover")
			}
			repaired.Stop()
			disk, err = store.LoadChecked(spec.Name)
			if err != nil || disk.Activation.Result != "success" {
				t.Fatalf("recovery result missing: %+v %v", disk, err)
			}
		})
	}
}

func crashState(result string) Runtime {
	stamp := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	return Runtime{LastActual: stamp, Activation: Activation{ID: "crash-intent", Unit: "work.service", Result: result, Scheduled: stamp, Actual: stamp}}
}

func TestTimerStoreProcessCrashAtReplacement(t *testing.T) {
	for _, stage := range []string{"before", "after"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save("work.timer", crashState("pending")); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestTimerStateCrashHelper$")
			cmd.Env = append(os.Environ(), "WINUNITD_TIMER_CRASH_DIR="+dir, "WINUNITD_TIMER_CRASH_STAGE="+stage)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 67 {
				t.Fatalf("crash helper: %v %s", err, output)
			}
			expected := "pending"
			if stage == "after" {
				expected = "success"
			}
			got, err := store.LoadChecked("work.timer")
			if err != nil || got != crashState(expected) {
				t.Fatalf("incomplete state after %s crash: %+v %v", stage, got, err)
			}
			// Orphaned pre-rename temporary files must never be treated as committed state.
			if err := store.Save("work.timer", crashState("failed")); err != nil {
				t.Fatal(err)
			}
			got, err = store.LoadChecked("work.timer")
			if err != nil || got != crashState("failed") {
				t.Fatal("post-crash replacement failed")
			}
		})
	}
}

func TestTimerStateCrashHelper(t *testing.T) {
	dir := os.Getenv("WINUNITD_TIMER_CRASH_DIR")
	if dir == "" {
		return
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ops := defaultStateFileOps()
	replace := ops.replace
	ops.replace = func(from, to string) error {
		if os.Getenv("WINUNITD_TIMER_CRASH_STAGE") == "before" {
			os.Exit(67)
		}
		if err := replace(from, to); err != nil {
			return err
		}
		os.Exit(67)
		return io.ErrUnexpectedEOF
	}
	store.files = &ops
	if err := store.Save("work.timer", crashState("success")); err != nil {
		t.Fatal(err)
	}
	t.Fatal("helper did not stop at replacement boundary")
}
