//go:build windows

package journal_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/journal"
)

const retentionChildBudget = 45 * time.Second

// Run separately so unrelated parallel tests cannot obscure native handle
// growth. The same entry point can be qualified as SYSTEM or a standard user.
func TestJournalRetentionNative(t *testing.T) {
	if os.Getenv("WINUNITD_RETENTION_CHILD") != "1" {
		out, err := runRetentionChild(t)
		t.Logf("isolated journal retention: %s", out)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), retentionChildBudget)
	defer cancel()
	s, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var first pressureMemory
	for cycle := 0; cycle < 3; cycle++ {
		for i := 0; i < 512; i++ {
			name := fmt.Sprintf("churn-%d-%d.service", cycle, i)
			capture := s.Attach(name, 42, "native-retention", strings.NewReader("retained\n"), nil)
			if !capture.WaitContext(ctx) {
				t.Fatal("capture failed", name)
			}
		}
		runtime.GC()
		got, err := samplePressureMemory()
		if err != nil {
			t.Fatal(err)
		}
		files, names := s.RetentionTestCounts()
		t.Logf("cycle=%d completedNames=%d files=%d counterNames=%d memory=%+v", cycle+1, (cycle+1)*512, files, names, got)
		if files != 0 || names != 0 {
			t.Fatal("completed healthy names retained state")
		}
		if cycle == 0 {
			first = got
		}
		if got.Handles > first.Handles+32 || got.HeapBytes > first.HeapBytes+(8<<20) {
			t.Fatalf("name churn resource growth: first=%+v current=%+v", first, got)
		}
	}
	entries, err := s.Read("churn-0-0.service")
	if err != nil || len(entries) != 1 || entries[0].Message != "retained" {
		t.Fatal("retired file not queryable", err)
	}
}

// runRetentionChild starts the isolated process and waits on a timer the test
// goroutine can leave. Cmd.Wait blocks in WaitForSingleObject(INFINITE) until
// the process exits, so a child that outlives the package deadline stalls
// every test parked on t.Parallel. The wait is the child budget, or earlier
// when the package deadline is close, and the child is killed if it is still
// alive.
func runRetentionChild(t *testing.T) ([]byte, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	limit := retentionChildBudget + 5*time.Second
	if deadline, ok := t.Deadline(); ok {
		if remain := time.Until(deadline) - 8*time.Second; remain < limit {
			limit = remain
		}
	}
	if limit < time.Second {
		return nil, fmt.Errorf("not enough time left for the retention child")
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestJournalRetentionNative$", "-test.v", "-test.timeout="+retentionChildBudget.String())
	cmd.Env = append(os.Environ(), "WINUNITD_RETENTION_CHILD=1")
	cmd.WaitDelay = 3 * time.Second
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buf.Bytes(), err
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case err := <-done:
			if err == nil {
				return buf.Bytes(), nil
			}
			return buf.Bytes(), fmt.Errorf("retention child exceeded %s: %w", limit, err)
		case <-time.After(3 * time.Second):
			return nil, fmt.Errorf("retention child did not exit after kill (%s)", limit)
		}
	}
}
