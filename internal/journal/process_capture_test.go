package journal

import (
	"context"
	"fmt"
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
	mode := os.Getenv(captureChildEnv)
	if mode != "1" && mode != "quiet" {
		t.Skip("subprocess helper")
	}
	if mode == "quiet" {
		if _, err := io.WriteString(os.Stdout, "quiet during saturation\n"); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
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

func TestConcurrentChildrenPreserveQuietOutputDuringStorageStall(t *testing.T) {
	s := testStore(t)
	blocked, release := make(chan struct{}), make(chan struct{})
	var once, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	defer unblock()
	s.onOpen = func() { once.Do(func() { close(blocked); <-release }) }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var captures []*Capture
	launch := func(name, mode string) <-chan error {
		out, outWrite, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { out.Close(); outWrite.Close() })
		stderr, errWrite, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { stderr.Close(); errWrite.Close() })
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestJournalCaptureChild$")
		cmd.Env = append(os.Environ(), captureChildEnv+"="+mode)
		cmd.Stdout, cmd.Stderr = outWrite, errWrite
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		outWrite.Close()
		errWrite.Close()
		captures = append(captures, s.Attach(name, cmd.Process.Pid, name, out, stderr))
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		return done
	}
	var children []<-chan error
	for i := 0; i < 5; i++ {
		children = append(children, launch(fmt.Sprintf("producer-%d.service", i), "1"))
	}
	for _, done := range children {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("storage stall blocked child exit")
		}
	}
	select {
	case <-blocked:
	default:
		t.Fatal("storage was not exercised")
	}
	quiet := launch("quiet.service", "quiet")
	select {
	case err := <-quiet:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("quiet child did not exit")
	}
	for {
		s.queueMu.Lock()
		quietQueued := false
		for _, q := range s.captureQueues {
			if q.items.Front().Value.(captureWrite).entry.Unit == "quiet.service" {
				quietQueued = true
			}
			if q.group.bytes.Load() > invocationQueueBytes {
				t.Error("invocation escaped byte budget")
			}
		}
		bounded := s.queuedBytes <= captureQueueBytes && s.queuedRecords <= captureQueueRecords
		s.queueMu.Unlock()
		if !bounded {
			t.Fatal("aggregate capture escaped budget")
		}
		if quietQueued {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("quiet output was crowded out")
		case <-time.After(time.Millisecond):
		}
	}
	if s.CaptureStats("quiet.service").DroppedRecords != 0 {
		t.Fatal("quiet output was discarded")
	}
	unblock()
	for _, capture := range captures {
		if !capture.WaitContext(ctx) {
			t.Fatal("capture completion did not recover")
		}
	}
	entries, err := s.Read("quiet.service")
	if err != nil || len(entries) != 1 || entries[0].Message != "quiet during saturation" {
		t.Fatalf("quiet journal: %+v, %v", entries, err)
	}
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
