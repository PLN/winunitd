package runtime

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAssignDaemonPIDNil(t *testing.T) {
	t.Parallel()
	if err := assignDaemonPID(nil, 1); err != nil {
		t.Fatal(err)
	}
}

func TestAssignDaemonPIDDoesNotSwallowClosed(t *testing.T) {
	t.Parallel()
	j, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	err = assignDaemonPID(j, 1)
	if err == nil {
		t.Fatal("closed daemon job must surface AssignPID error")
	}
	if errors.Is(err, errAlreadyInJob) {
		t.Fatal("closed job is not nesting-denied")
	}
	if !strings.Contains(err.Error(), "not already-in-job/nesting") {
		t.Fatalf("non-nesting error must be returned, not swallowed: %v", err)
	}
}

func TestAssignDaemonPIDInvalidPID(t *testing.T) {
	t.Parallel()
	j, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	err = assignDaemonPID(j, 0)
	if err == nil {
		t.Fatal("pid 0 must fail")
	}
	if errors.Is(err, errAlreadyInJob) {
		t.Fatal("invalid pid is not nesting-denied")
	}
	err = assignDaemonPID(j, -1)
	if err == nil {
		t.Fatal("negative pid must fail")
	}
}

func TestAssignDaemonPIDIgnoresAlreadyInJob(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("assign pid 4 to daemon job: %w", errAlreadyInJob)
	if !errors.Is(wrapped, errAlreadyInJob) {
		t.Fatal("sentinel must unwrap")
	}
}

func TestDaemonJobConcurrentAssignAndClose(t *testing.T) {
	j, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 40; n++ {
				_ = j.AssignPID(1)
				_ = j.Closed()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(time.Millisecond)
		_ = j.Close()
	}()
	wg.Wait()
	_ = j.Close()
	if !j.Closed() {
		t.Fatal("Close must mark the job closed")
	}
}
