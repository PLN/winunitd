//go:build windows

package runtime

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestBootstrapRetainsEachFailedHandleClose(t *testing.T) {
	for slot, name := range []string{"stdin", "stdout-writer", "stderr-writer", "stdout-reader", "stderr-reader"} {
		t.Run(name, func(t *testing.T) {
			var protected windows.Handle
			protect := func(h windows.Handle) {
				if err := windows.SetHandleInformation(h, protectHandleFromClose, protectHandleFromClose); err != nil {
					t.Fatal(err)
				}
				protected = h
			}
			calls := 0
			pipe := func() (windows.Handle, windows.Handle, error) {
				r, w, err := makeStdPipe()
				if err == nil {
					if slot == 1+calls {
						protect(w)
					}
					if slot == 3+calls {
						protect(r)
					}
				}
				calls++
				return r, w, err
			}
			input := func() (windows.Handle, error) {
				h, err := openNUL()
				if err == nil && slot == 0 {
					protect(h)
				}
				return h, err
			}
			p, cause := (&winLauncher{}).createWith(StartSpec{Argv: []string{filepath.Join(t.TempDir(), "missing.exe")}}, OpenUnitJobWith, pipe, input)
			if p == nil {
				t.Fatal("missing bootstrap owner")
			}
			t.Cleanup(func() {
				if p.launchHandles[slot] == protected {
					_ = windows.SetHandleInformation(protected, protectHandleFromClose, 0)
				}
				_ = p.Stop(time.Second)
			})
			if cause == nil || p.PID() != 0 {
				t.Fatal("expected pre-process launch failure")
			}
			retained, err := failedProcessStart(p, cause)
			if retained != p || err == nil {
				t.Fatal("failed cleanup lost bootstrap ownership")
			}
			for i, h := range p.launchHandles {
				if i == slot && h != protected {
					t.Fatal("failed handle was discarded")
				}
				if i != slot && h != 0 {
					t.Fatal("independent handle cleanup was skipped")
				}
			}
			if err := windows.SetHandleInformation(protected, protectHandleFromClose, 0); err != nil {
				t.Fatal(err)
			}
			if err := p.Stop(time.Second); err != nil {
				t.Fatal(err)
			}
			if _, err := windows.GetFileType(protected); err == nil {
				t.Fatal("successful bootstrap cleanup leaked a handle")
			}
			if p.job.handle != 0 || !p.closed {
				t.Fatal("bootstrap cleanup did not finish")
			}
		})
	}
}

func TestPartialLaunchOpenRetainsFailedCleanup(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			var retainedHandle windows.Handle
			calls := 0
			pipe := func() (windows.Handle, windows.Handle, error) {
				r, w, err := makeStdPipe()
				calls++
				if err == nil && calls == failAt {
					retainedHandle = r
					if err := windows.SetHandleInformation(r, protectHandleFromClose, protectHandleFromClose); err != nil {
						t.Fatal(err)
					}
					return r, w, errors.New("injected pipe setup failure")
				}
				return r, w, err
			}
			input := func() (windows.Handle, error) {
				h, err := openNUL()
				if err == nil && failAt == 3 {
					retainedHandle = h
					if err := windows.SetHandleInformation(h, protectHandleFromClose, protectHandleFromClose); err != nil {
						t.Fatal(err)
					}
					return h, errors.New("injected input setup failure")
				}
				return h, err
			}
			p, cause := (&winLauncher{}).createWith(StartSpec{Argv: []string{"unused.exe"}}, OpenUnitJobWith, pipe, input)
			t.Cleanup(func() {
				if !p.closed {
					_ = windows.SetHandleInformation(retainedHandle, protectHandleFromClose, 0)
					_ = p.Stop(time.Second)
				}
			})
			retained, err := failedProcessStart(p, cause)
			if retained != p || err == nil || p.PID() != 0 {
				t.Fatal("partial pipe cleanup was not retained")
			}
			if err := windows.SetHandleInformation(retainedHandle, protectHandleFromClose, 0); err != nil {
				t.Fatal(err)
			}
			if err := p.Stop(time.Second); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnitJobSetupRetainsFailedClose(t *testing.T) {
	var h windows.Handle
	job, err := openUnitJobWith(JobLimits{CPURate: 10001}, func(sa *windows.SecurityAttributes, name *uint16) (windows.Handle, error) {
		var err error
		h, err = windows.CreateJobObject(sa, name)
		if err == nil {
			err = windows.SetHandleInformation(h, protectHandleFromClose, protectHandleFromClose)
		}
		return h, err
	})
	t.Cleanup(func() {
		if job != nil && job.handle != 0 {
			_ = windows.SetHandleInformation(h, protectHandleFromClose, 0)
			_ = job.Close()
		}
	})
	if err == nil || job == nil || job.handle != h {
		t.Fatal("failed job setup discarded native close ownership")
	}
	if err := windows.SetHandleInformation(h, protectHandleFromClose, 0); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if job.handle != 0 {
		t.Fatal("job cleanup retry did not release handle")
	}
}

func TestPostCreationWriterCloseFailureTerminatesSuspendedProcess(t *testing.T) {
	var writer windows.Handle
	pipe := func() (windows.Handle, windows.Handle, error) {
		r, w, err := makeStdPipe()
		if err == nil && writer == 0 {
			writer = w
			if err := windows.SetHandleInformation(w, protectHandleFromClose, protectHandleFromClose); err != nil {
				t.Fatal(err)
			}
		}
		return r, w, err
	}
	p, cause := (&winLauncher{}).createWith(StartSpec{Argv: []string{testAbs(t), winunitdHelperArgPrefix + "sleep"}, Env: helperEnv("WINUNITD_JOB_HELPER=sleep")}, OpenUnitJobWith, pipe, openNUL)
	if p == nil {
		t.Fatal("missing created process")
	}
	t.Cleanup(func() {
		if p.launchHandles[1] == writer {
			_ = windows.SetHandleInformation(writer, protectHandleFromClose, 0)
		}
		_ = p.Stop(time.Second)
	})
	if cause == nil || p.PID() == 0 {
		t.Fatal("expected post-creation writer cleanup failure")
	}
	retained, err := failedProcessStart(p, cause)
	if retained != p || err == nil || p.Alive() {
		t.Fatal("suspended process was not terminated with handles retained")
	}
	if err := windows.SetHandleInformation(writer, protectHandleFromClose, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
}
