package servicing

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/servicing/servicingtest"
)

// A floor change excludes every other change from reading the record until
// it has written or removed it. Readers do not wait.
func TestFloorChangeExcludesOtherChanges(t *testing.T) {
	_, path := floorDir(t)
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.5.0"}); err != nil {
		t.Fatal(err)
	}
	held, err := BeginFloorChange(path, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BeginFloorChange(path, 0); !errors.Is(err, ErrFloorBusy) {
		t.Fatalf("second change: %v", err)
	}
	start := time.Now()
	if _, err := BeginFloorChange(path, 100*time.Millisecond); !errors.Is(err, ErrFloorBusy) || time.Since(start) < 100*time.Millisecond {
		t.Fatalf("bounded wait: %v after %v", err, time.Since(start))
	}
	if f, err := ReadFloor(path); err != nil || f.MinVersion != "0.5.0" {
		t.Fatalf("reader during a change: %+v %v", f, err)
	}
	// A waiting change reads what the holder wrote, not what was there
	// when it began waiting.
	seen := make(chan *Floor, 1)
	go func() {
		c, err := BeginFloorChange(path, DefaultLockWait)
		if err != nil {
			seen <- nil
			return
		}
		f, _ := c.Current()
		_ = c.Remove()
		_ = c.Close()
		seen <- f
	}()
	time.Sleep(50 * time.Millisecond)
	if err := held.Write(&Floor{Schema: 1, MinVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if f := <-seen; f == nil || f.MinVersion != "0.1.0" {
		t.Fatalf("waiting change read %+v", f)
	}
	if f, err := ReadFloor(path); err != nil || f != nil {
		t.Fatalf("after the waiting change: %+v %v", f, err)
	}
}

// Without a daemon directory no change can begin, and clearing is a no-op.
func TestFloorChangeNeedsTheDaemonDirectory(t *testing.T) {
	path := FloorPath(servicingtest.Root(t))
	if _, err := BeginFloorChange(path, 0); !errors.Is(err, ErrNoDataDirectory) {
		t.Fatalf("change without a daemon directory: %v", err)
	}
	if err := RemoveFloor(path); err != nil {
		t.Fatal(err)
	}
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.1.0"}); !errors.Is(err, ErrNoDataDirectory) {
		t.Fatalf("write without a daemon directory: %v", err)
	}
}

// Changes made concurrently never overlap, and every one completes.
func TestFloorChangesNeverOverlap(t *testing.T) {
	_, path := floorDir(t)
	var inside, overlaps atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 10 {
				c, err := BeginFloorChange(path, DefaultLockWait)
				if err != nil {
					errs <- err
					return
				}
				if inside.Add(1) != 1 {
					overlaps.Add(1)
				}
				_, rerr := c.Current()
				time.Sleep(time.Millisecond)
				var werr error
				if (w+i)%3 == 0 {
					werr = c.Remove()
				} else {
					werr = c.Write(&Floor{Schema: 1, MinVersion: fmt.Sprintf("0.%d.%d", w, i)})
				}
				inside.Add(-1)
				if err := errors.Join(rerr, werr, c.Close()); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if overlaps.Load() != 0 {
		t.Fatalf("%d changes overlapped", overlaps.Load())
	}
	if _, err := ReadFloor(path); err != nil {
		t.Fatal(err)
	}
}

// The lock holds across processes and is released when its holder exits,
// even without closing it.
func TestFloorLockIsHeldAcrossProcesses(t *testing.T) {
	if path := os.Getenv("WINUNITD_TEST_HOLD_FLOOR_LOCK"); path != "" {
		c, err := BeginFloorChange(path, 0)
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(2)
		}
		fmt.Println("locked")
		// Hold until killed; the exit must release the lock.
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		_ = c.Close()
		os.Exit(0)
	}
	_, path := floorDir(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestFloorLockIsHeldAcrossProcesses$")
	cmd.Env = append(os.Environ(), "WINUNITD_TEST_HOLD_FLOOR_LOCK="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("holder: %q %v", line, err)
	}
	if _, err := BeginFloorChange(path, 0); !errors.Is(err, ErrFloorBusy) {
		t.Fatalf("change while another process holds the lock: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	c, err := BeginFloorChange(path, 5*time.Second)
	if err != nil {
		t.Fatalf("lock after its holder exited: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
