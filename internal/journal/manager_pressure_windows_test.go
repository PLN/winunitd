//go:build windows

package journal_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
	"golang.org/x/sys/windows"
)

func TestManagerPressureChild(t *testing.T) {
	mode, root, name := os.Getenv("WINUNITD_PRESSURE_MODE"), os.Getenv("WINUNITD_PRESSURE_ROOT"), os.Getenv("WINUNITD_PRESSURE_NAME")
	if mode == "" {
		t.Skip("isolated child helper")
	}
	if mode == "quiet" {
		if _, err := io.WriteString(os.Stdout, "quiet manager pressure\n"); err != nil {
			os.Exit(2)
		}
	} else {
		chunk := strings.Repeat("x", 64<<10)
		if mode == "lines" {
			chunk = strings.Repeat(strings.Repeat("x", 127)+"\n", 512)
		}
		for i := 0; i < 68; i++ {
			if _, err := io.WriteString(os.Stdout, chunk); err != nil {
				os.Exit(3)
			}
			if _, err := io.WriteString(os.Stderr, chunk); err != nil {
				os.Exit(4)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(root, name+".ready"), []byte("output emitted"), 0600); err != nil {
		os.Exit(5)
	}
	for {
		time.Sleep(time.Hour)
	}
}

type pressureMemory struct {
	HeapBytes    uint64 `json:"heapBytes"`
	PrivateBytes uint64 `json:"privateBytes"`
	Resident     uint64 `json:"residentBytes"`
	Goroutines   int    `json:"goroutines"`
	Handles      uint32 `json:"handles"`
	Threads      int    `json:"threads"`
}

var pressureGetMemory = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
var pressureGetHandles = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

func pressureThreads() (int, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	count := 0
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID == uint32(os.Getpid()) {
			count++
		}
	}
	if err != windows.ERROR_NO_MORE_FILES {
		return 0, err
	}
	return count, nil
}

// PROCESS_MEMORY_COUNTERS_EX: PrivateUsage is committed private bytes, while
// WorkingSetSize is resident memory. Keep both rather than calling either heap.
// https://learn.microsoft.com/en-us/windows/win32/api/psapi/ns-psapi-process_memory_counters_ex
func samplePressureMemory() (pressureMemory, error) {
	var counters struct {
		Size, Faults uint32
		Values       [9]uintptr
	}
	counters.Size = uint32(unsafe.Sizeof(counters))
	if ok, _, err := pressureGetMemory.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size)); ok == 0 {
		return pressureMemory{}, err
	}
	var handles uint32
	if ok, _, err := pressureGetHandles.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&handles))); ok == 0 {
		return pressureMemory{}, err
	}
	threads, err := pressureThreads()
	if err != nil {
		return pressureMemory{}, err
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return pressureMemory{HeapBytes: memory.HeapAlloc, PrivateBytes: uint64(counters.Values[8]), Resident: uint64(counters.Values[1]), Goroutines: runtime.NumGoroutine(), Handles: handles, Threads: threads}, nil
}

func TestManagerAggregateCapturePressure(t *testing.T) {
	if os.Getenv("WINUNITD_PRESSURE_MANAGER") != "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestManagerAggregateCapturePressure$", "-test.v")
		// Use the same constrained Go scheduler on large development hosts and
		// hosted workers. Native I/O and all six workload processes still overlap.
		cmd.Env = append(os.Environ(), "WINUNITD_PRESSURE_MANAGER=1", "GOMAXPROCS=2")
		output, err := cmd.CombinedOutput()
		t.Logf("isolated manager pressure: %s", output)
		if err != nil {
			t.Fatalf("isolated pressure process: %v", err)
		}
		return
	}
	var after [3]pressureMemory
	for i := range after {
		if !t.Run(fmt.Sprintf("cycle-%d", i+1), func(t *testing.T) {
			after[i] = runManagerPressureCycle(t)
		}) {
			return
		}
	}
	// Go caches Windows threads and their synchronization handles after native
	// pipe/process waits. Bound the cold run below, then require repeated equal
	// workloads to settle without accumulating another set of native resources.
	if after[2].Handles > after[0].Handles+32 || after[2].Threads > after[0].Threads+4 {
		t.Fatalf("repeated workload resource growth: first=%+v final=%+v", after[0], after[2])
	}
}

func runManagerPressureCycle(t *testing.T) pressureMemory {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "units"), 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for i := 0; i < 6; i++ {
		name, mode := fmt.Sprintf("pressure-%d.service", i), "fragments"
		if i%2 == 1 {
			mode = "lines"
		}
		if i == 5 {
			name, mode = "pressure-quiet.service", "quiet"
		}
		body := fmt.Sprintf("[Service]\nExecStart=%s\nExecStartArg=-test.run=^TestManagerPressureChild$\nEnvironment=WINUNITD_PRESSURE_MODE=%s\nEnvironment=\"WINUNITD_PRESSURE_ROOT=%s\"\nEnvironment=WINUNITD_PRESSURE_NAME=%s\nTimeoutStartSec=20s\nTimeoutStopSec=1s\n", exe, mode, base, name)
		if err := os.WriteFile(filepath.Join(base, "units", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	blocked, release := make(chan struct{}), make(chan struct{})
	var entered, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	var store *journal.Store
	m, err := manager.New(manager.Config{BaseDir: base, JournalOpen: func(dir string) (*journal.Store, error) {
		var openErr error
		store, openErr = journal.OpenPressureTestStore(dir, func() { entered.Do(func() { close(blocked); <-release }) })
		return store, openErr
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		if err := m.CloseContext(ctx); err != nil {
			t.Error(err)
		}
	})
	if result, err := m.Reload(); err != nil || len(result.Errors) != 0 {
		t.Fatalf("reload: %+v, %v", result, err)
	}
	runtime.GC()
	baseline, err := samplePressureMemory()
	if err != nil {
		t.Fatal(err)
	}
	peak := baseline
	var maxStatus time.Duration
	observe := func() {
		now := time.Now()
		if _, err := m.Status(""); err != nil {
			t.Fatal(err)
		}
		maxStatus = max(maxStatus, time.Since(now))
		got, err := samplePressureMemory()
		if err != nil {
			t.Fatal(err)
		}
		peak.HeapBytes = max(peak.HeapBytes, got.HeapBytes)
		peak.PrivateBytes = max(peak.PrivateBytes, got.PrivateBytes)
		peak.Resident = max(peak.Resident, got.Resident)
		peak.Goroutines = max(peak.Goroutines, got.Goroutines)
		peak.Handles = max(peak.Handles, got.Handles)
		peak.Threads = max(peak.Threads, got.Threads)
	}
	wait := func(check func() bool) {
		deadline := time.Now().Add(30 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				t.Fatal("pressure phase timed out")
			}
			observe()
			time.Sleep(10 * time.Millisecond)
		}
		observe()
	}
	for _, name := range names[:5] {
		if _, err := m.Start(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	wait(func() bool {
		for _, name := range names[:5] {
			if _, err := os.Stat(filepath.Join(base, name+".ready")); err != nil {
				return false
			}
		}
		return true
	})
	select {
	case <-blocked:
	default:
		t.Fatal("storage did not stall")
	}
	quiet := names[5]
	if _, err := m.Start(context.Background(), quiet); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return store.PressureTestQueue().ByUnit[quiet] != 0 })
	queue := store.PressureTestQueue()
	if queue.Bytes > 16<<20 || queue.Records > 16384 || queue.Groups > 6 {
		t.Fatalf("queue bounds: %+v", queue)
	}
	for _, bytes := range queue.ByUnit {
		if bytes > 4<<20 {
			t.Fatal("invocation escaped byte budget")
		}
	}
	var dropped uint64
	for _, name := range names[:5] {
		dropped += store.CaptureStats(name).DroppedBytes
	}
	if dropped == 0 || store.CaptureStats(quiet).DroppedRecords != 0 {
		t.Fatal("loss accounting or quiet fairness failed")
	}
	stops := make(chan error, len(names))
	stopStart := time.Now()
	for _, name := range names {
		go func() { _, err := m.Stop(name); stops <- err }()
	}
	wait(func() bool { return len(stops) == len(names) })
	stopDuration := time.Since(stopStart)
	for _, name := range names {
		if err := <-stops; err == nil {
			t.Fatal("stop reported complete output while storage remained stalled")
		}
		status, err := m.Status(name)
		if err != nil || status.Unit.MainPID != 0 || !status.Unit.TerminationUncertain {
			t.Fatalf("retained output cleanup: %+v, %v", status, err)
		}
	}
	if maxStatus > time.Second || stopDuration > 5*time.Second {
		t.Fatalf("control stalled: status=%v stop=%v", maxStatus, stopDuration)
	}
	if peak.HeapBytes > baseline.HeapBytes+(128<<20) || peak.PrivateBytes > baseline.PrivateBytes+pressurePrivateBudget || peak.Goroutines > baseline.Goroutines+256 || peak.Handles > baseline.Handles+256 || peak.Threads > baseline.Threads+64 {
		t.Fatalf("manager resource budget: baseline=%+v peak=%+v", baseline, peak)
	}
	unblock()
	// Releasing storage does not synchronously persist the queued backlog.
	// Each stop retains its one-second budget; retry retained cleanup within a
	// separate bounded recovery phase instead of assuming one retry can drain it.
	recoveryStart := time.Now()
	recoveryDeadline := recoveryStart.Add(20 * time.Second)
	for _, name := range names {
		for {
			if _, err := m.Stop(name); err == nil {
				break
			} else if time.Now().After(recoveryDeadline) {
				t.Fatalf("storage recovery did not complete: %v", err)
			}
			status, err := m.Status(name)
			if err != nil || status.Unit.MainPID != 0 || !status.Unit.TerminationUncertain {
				t.Fatalf("lost cleanup during storage recovery: %+v, %v", status, err)
			}
		}
	}
	recoveryDuration := time.Since(recoveryStart)
	logs, err := m.Logs(protocol.LogsParams{Unit: quiet})
	if err != nil || len(logs.Entries) != 1 || logs.Entries[0].Message != "quiet manager pressure" {
		t.Fatalf("quiet output after recovery: %+v, %v", logs, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	after, err := samplePressureMemory()
	if err != nil {
		t.Fatal(err)
	}
	result := struct {
		Baseline, Peak, After           pressureMemory
		QueueBytes                      int64
		QueueRecords                    int
		DroppedBytes                    uint64
		MaxStatusMS, StopMS, RecoveryMS float64
	}{baseline, peak, after, queue.Bytes, queue.Records, dropped, float64(maxStatus) / float64(time.Millisecond), float64(stopDuration) / float64(time.Millisecond), float64(recoveryDuration) / float64(time.Millisecond)}
	encoded, _ := json.Marshal(result)
	t.Logf("manager pressure measurements: %s", encoded)
	if after.Goroutines > baseline.Goroutines+16 {
		t.Fatalf("cleanup did not release workers: baseline=%+v after=%+v", baseline, after)
	}
	return after
}
