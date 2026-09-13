//go:build windows

package journal_test

import (
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

// Run separately so unrelated parallel tests cannot obscure native handle
// growth. The same entry point can be qualified as SYSTEM or a standard user.
func TestJournalRetentionNative(t *testing.T) {
	if os.Getenv("WINUNITD_RETENTION_CHILD") != "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestJournalRetentionNative$", "-test.v")
		cmd.Env = append(os.Environ(), "WINUNITD_RETENTION_CHILD=1")
		out, err := cmd.CombinedOutput()
		t.Logf("isolated journal retention: %s", out)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
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
			if !capture.WaitContext(context.Background()) {
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
