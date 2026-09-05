package journal

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

type partialFailureWriter struct {
	file      *os.File
	remaining int
	err       error
}

func (w *partialFailureWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		p = p[:w.remaining]
	}
	n, err := w.file.Write(p)
	w.remaining -= n
	if err != nil {
		return n, err
	}
	return n, w.err
}

func TestPartialStorageFailurePreservesNextRecordAfterReopen(t *testing.T) {
	for _, failure := range []error{syscall.ENOSPC, io.ErrShortWrite} {
		t.Run(failure.Error(), func(t *testing.T) {
			s := testStore(t)
			const name = "worker.service"
			if err := s.append(Entry{Unit: name, Message: "before"}); err != nil {
				t.Fatal(err)
			}
			if err := s.syncUnit(name); err != nil {
				t.Fatal(err)
			}
			u := s.fileExisting(name)
			u.mu.Lock()
			u.w = bufio.NewWriter(&partialFailureWriter{file: u.f, remaining: 37, err: failure})
			u.mu.Unlock()
			s.Attach(name, 42, "example-invocation", strings.NewReader(strings.Repeat("x", 8192)+"\nafter failure\n"), nil)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if s.WaitContext(ctx, name) {
				t.Fatal("partial write failure reported successful persistence")
			}
			stats := s.CaptureStats(name)
			if stats.DroppedRecords != 2 || stats.StorageErrors == 0 {
				t.Fatalf("unreported capture loss: %+v", stats)
			}
			if err := s.Close(); err == nil {
				t.Fatal("failed buffered writer closed successfully")
			}
			damaged, err := os.ReadFile(s.path(name))
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(s.Dir())
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err := reopened.append(Entry{Unit: name, Message: "recovered"}); err != nil {
				t.Fatal(err)
			}
			entries, err := reopened.Read(name)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 || entries[0].Message != "before" || entries[1].Message != "recovered" {
				t.Fatalf("record recovery: %+v", entries)
			}
			raw, err := os.ReadFile(s.path(name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(raw, damaged) || raw[len(damaged)] != '\n' {
				t.Fatal("recovery rewrote existing bytes or joined records")
			}
			if reopened.CaptureStats(name).StorageErrors != 1 {
				t.Fatal("boundary repair was not reported")
			}
		})
	}
}

func TestRecoveryPreservesCompleteRecordWithoutNewline(t *testing.T) {
	s := testStore(t)
	const name = "worker.service"
	if err := s.append(Entry{Unit: name, Message: "before"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.path(name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path(name), bytes.TrimSuffix(raw, []byte{'\n'}), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.append(Entry{Unit: name, Message: "after"}); err != nil {
		t.Fatal(err)
	}
	entries, err := reopened.Read(name)
	if err != nil || len(entries) != 2 || entries[0].Message != "before" || entries[1].Message != "after" {
		t.Fatalf("complete final record was lost: entries=%+v err=%v", entries, err)
	}
}
