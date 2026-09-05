package journal

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const captureChildEnv = "WINUNITD_JOURNAL_TEST_CHILD"

func TestJournalCaptureChild(t *testing.T) {
	if os.Getenv(captureChildEnv) != "1" {
		t.Skip("subprocess helper")
	}
	chunk := strings.Repeat("x", MaxCaptureFragment)
	for i := 0; i < invocationQueueBytes/MaxCaptureFragment+4; i++ {
		if _, err := io.WriteString(os.Stdout, chunk); err != nil {
			os.Exit(2)
		}
		if _, err := io.WriteString(os.Stderr, chunk); err != nil {
			os.Exit(3)
		}
	}
	os.Exit(0)
}

// Exercise actual OS pipes and child exit with storage stalled. Parent-owned
// readers stay open after cmd.Wait so trailing bytes remain available to capture.
func TestChildOutputDrainsDuringStorageStall(t *testing.T) {
	s := testStore(t)
	blocked, release := make(chan struct{}), make(chan struct{})
	var entered, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	defer unblock()
	s.onOpen = func() { entered.Do(func() { close(blocked) }); <-release }
	stdout, outWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	defer outWrite.Close()
	stderr, errWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	defer errWrite.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestJournalCaptureChild$")
	cmd.Env = append(os.Environ(), captureChildEnv+"=1")
	cmd.Stdout, cmd.Stderr = outWrite, errWrite
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = outWrite.Close()
	_ = errWrite.Close()
	s.Attach("output.service", cmd.Process.Pid, "example-invocation", stdout, stderr)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("child did not reach journal storage")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("child failed while draining output", err)
		}
	case <-ctx.Done():
		t.Fatal("stalled journal prevented child exit")
	}
	if ctx.Err() != nil {
		t.Fatal("child only exited after its deadline")
	}
	stats := s.CaptureStats("output.service")
	if stats.DroppedRecords == 0 || stats.DroppedBytes == 0 {
		t.Fatal("large child output did not report overflow")
	}
	s.queueMu.Lock()
	pendingBytes := s.queuedBytes
	s.queueMu.Unlock()
	if pendingBytes > invocationQueueBytes {
		t.Fatalf("child exceeded queued message budget: %d", pendingBytes)
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer waitCancel()
	if s.WaitContext(waitCtx, "output.service") {
		t.Fatal("stalled persistence reported success")
	}
	unblock()
	if err := s.Close(); err != nil {
		t.Fatal("journal did not recover", err)
	}
}
