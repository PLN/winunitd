//go:build windows

package journal_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/manager"
	"golang.org/x/sys/windows"
)

func TestManagerCombinedOperationalPressure(t *testing.T) {
	runOperationalPressure(t, false)
}

// This lane must run on a separately prepared tiny lab volume. Ordinary CI
// still runs the combined injected short-write/disk-full lane above.
func TestManagerCombinedDisposableDiskPressure(t *testing.T) {
	journal.DisposableTestVolume(t)
	runOperationalPressure(t, true)
}

func runOperationalPressure(t *testing.T, nativeDisk bool) {
	if os.Getenv("WINUNITD_OPERATIONAL_MANAGER") != "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, "-test.run=^"+t.Name()+"$", "-test.v")
		cmd.Env = append(os.Environ(), "WINUNITD_OPERATIONAL_MANAGER=1", "GOMAXPROCS=2")
		output, err := cmd.CombinedOutput()
		t.Logf("isolated combined pressure: %s", output)
		if err != nil {
			t.Fatalf("combined pressure process: %v", err)
		}
		return
	}
	var after [3]pressureMemory
	for i := range after {
		if !t.Run(fmt.Sprintf("cycle-%d", i+1), func(t *testing.T) {
			after[i] = runManagerPressureScenario(t, true, nativeDisk)
		}) {
			return
		}
	}
	if after[2].Handles > after[0].Handles+32 || after[2].Threads > after[0].Threads+4 {
		t.Fatalf("combined workload resource growth: first=%+v final=%+v", after[0], after[2])
	}
}

type operationalPressure struct {
	base, watch string
	journalDir  string
	entered     chan struct{}
	readers     chan struct{}
	once        sync.Once
	repair      func()
}

func (p *operationalPressure) prepareVolume(t *testing.T) {
	root, _ := journal.DisposableTestVolume(t)
	dir, err := os.MkdirTemp(root, "combined-pressure-")
	if err != nil {
		t.Fatal(err)
	}
	p.journalDir = dir
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
}

func (p *operationalPressure) fillVolume(t *testing.T) {
	root, total := journal.DisposableTestVolume(t)
	filler, err := os.CreateTemp(root, "combined-filler-")
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	p.repair = func() {
		once.Do(func() {
			if err := filler.Close(); err != nil {
				t.Error(err)
			}
			if err := os.Remove(filler.Name()); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(p.repair)
	chunk := bytes.Repeat([]byte{0xa5}, 4<<10)
	var written uint64
	for {
		n, err := filler.Write(chunk)
		written += uint64(n)
		if errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL) {
			break
		}
		if err != nil || n != len(chunk) || written > total {
			t.Fatalf("native filler: bytes=%d error=%v", written, err)
		}
	}
	t.Logf("combined native disk-full: volume bytes=%d filler bytes=%d", total, written)
}

func prepareOperationalPressure(t *testing.T, base, exe string) *operationalPressure {
	p := &operationalPressure{base: base, watch: filepath.Join(base, "watch"), entered: make(chan struct{}, 4), readers: make(chan struct{})}
	if err := os.Mkdir(p.watch, 0700); err != nil {
		t.Fatal(err)
	}
	units := map[string]string{
		"crash.service": fmt.Sprintf("[Unit]\nFormatVersion=2\nStartLimitIntervalSec=60s\nStartLimitBurst=4\n[Service]\nExecStart=%s\nExecStartArg=-test.run=^TestManagerPressureChild$\nEnvironment=WINUNITD_PRESSURE_MODE=crash\nEnvironment=\"WINUNITD_PRESSURE_ROOT=%s\"\nRestart=on-failure\nRestartSec=100ms\nTimeoutStartSec=10s\nTimeoutStopSec=1s\n", exe, base),
		"crash.path":    fmt.Sprintf("[Path]\nPathChanged=%s\n", p.watch),
	}
	for name, body := range units {
		if err := os.WriteFile(filepath.Join(base, "units", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func (p *operationalPressure) scan() {
	select {
	case <-p.readers:
		return
	default:
	}
	p.entered <- struct{}{}
	<-p.readers
}

func (p *operationalPressure) release() {
	if p.repair != nil {
		p.repair()
	}
	p.once.Do(func() { close(p.readers) })
}

func (p *operationalPressure) startReaders(t *testing.T, store *journal.Store) {
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan error, 1)
		go func() {
			_, _, _, err := store.QueryPageContext(ctx, "reader.service", time.Time{}, "original", 1024)
			finished <- err
		}()
		select {
		case <-p.entered:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("slow reader was not admitted")
		}
		cancel()
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("reader cancellation: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("slow reader blocked caller cancellation")
		}
	}
	for i := 0; i < 128; i++ {
		if _, cursor, _, err := store.QueryPageContext(context.Background(), "reader.service", time.Time{}, "original", 1024); !errors.Is(err, journal.ErrQueryBusy) || cursor != "original" {
			t.Fatalf("slow reader overload lost admission or cursor: %q %v", cursor, err)
		}
	}
}

func (p *operationalPressure) burstAndFail(t *testing.T, m *manager.Manager, wait func(func() bool)) {
	if _, err := m.Start(context.Background(), "crash.path"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 128; i++ {
		if err := os.WriteFile(filepath.Join(p.watch, fmt.Sprintf("burst-%03d", i)), []byte("trigger"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	wait(func() bool {
		status, err := m.Status("crash.service")
		return err == nil && status.Unit.Reason == core.ReasonStartLimit && status.Unit.MainPID == 0 && len(status.Unit.PendingCleanup) == 0
	})
	if _, err := m.Stop("crash.path"); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(p.base, "crash-*"))
	if err != nil || len(files) != 4 {
		t.Fatalf("native failure limit: executions=%d error=%v", len(files), err)
	}
	if _, err := m.Stop("crash.service"); err != nil {
		t.Fatal(err)
	}
	t.Log("combined faults: 128 native path writes, four exit-7 processes, four retained slow scans, 128 rejected excess readers")
}

func (p *operationalPressure) verifyRecovery(t *testing.T, store *journal.Store, m *manager.Manager, wait func(func() bool)) {
	wait(func() bool {
		entries, _, _, err := store.QueryPageContext(context.Background(), "reader.service", time.Time{}, "", 1024)
		if errors.Is(err, journal.ErrQueryBusy) {
			return false
		}
		if err != nil || len(entries) != 1 || entries[0].Message != "before fault" {
			t.Fatalf("reader recovery: %d entries, %v", len(entries), err)
		}
		return true
	})
	entries, err := store.Read("pressure-0.service")
	if err != nil || len(entries) < 2 || entries[0].Message != "before fault" {
		t.Fatalf("partial disk-full write recovery: %d entries, %v", len(entries), err)
	}
	// The first interrupted fragment must resume at its unwritten suffix.
	// Duplicate prefixes, dropped suffixes and joined records alter this payload.
	if entries[1].Message != strings.Repeat("x", journal.MaxCaptureFragment) {
		t.Fatal("interrupted record was not recovered intact")
	}
	status, err := m.Status("crash.service")
	if err != nil || status.Unit.MainPID != 0 || status.Unit.TerminationUncertain {
		t.Fatalf("stopped trigger companion resurrected: %+v %v", status, err)
	}
	t.Logf("combined recovery: disk-full errors=%d, recovered first fragment bytes=%d", store.CaptureStats("pressure-0.service").StorageErrors, len(entries[1].Message))
}
