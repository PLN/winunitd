package journal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type shortOnceWriter struct {
	bytes.Buffer
	first bool
}

func (w *shortOnceWriter) Write(p []byte) (int, error) {
	if !w.first {
		w.first = true
		return w.Buffer.Write(p[:min(5, len(p))])
	}
	return w.Buffer.Write(p)
}

func TestShortWriteWithoutErrorRetainsExactSuffix(t *testing.T) {
	w := &shortOnceWriter{}
	b := &recordBuffer{writer: w}
	raw := []byte("first complete record\nsecond complete record\n")
	if err := b.append(raw, 42); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write result: %v", err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.Bytes(), raw) {
		t.Fatal("retry duplicated or dropped the unwritten suffix")
	}
	if count, _ := b.pendingLoss(); count != 0 {
		t.Fatal("persisted record remains pending")
	}
}

type recoveringWriter struct {
	file      *os.File
	fail      atomic.Bool
	remaining int
}

func (w *recoveringWriter) Write(p []byte) (int, error) {
	if !w.fail.Load() {
		return w.file.Write(p)
	}
	if len(p) > w.remaining {
		p = p[:w.remaining]
	}
	n, err := w.file.Write(p)
	w.remaining -= n
	if err != nil {
		return n, err
	}
	return n, syscall.ENOSPC
}

func TestStorageRecoversPendingRecordWithoutNewOutput(t *testing.T) {
	s := testStore(t)
	const name = "recover.service"
	if err := s.append(Entry{Unit: name, Message: "before"}); err != nil {
		t.Fatal(err)
	}
	if err := s.syncUnit(name); err != nil {
		t.Fatal(err)
	}
	u := s.fileExisting(name)
	u.mu.Lock()
	w := &recoveringWriter{file: u.f, remaining: 37}
	w.fail.Store(true)
	u.w = &recordBuffer{writer: w}
	u.mu.Unlock()
	message := strings.Repeat("retained", 2048)
	if err := s.append(Entry{Unit: name, Message: message}); err != nil {
		t.Fatal(err)
	}
	if err := s.syncUnit(name); err == nil {
		t.Fatal("disk-full flush reported success")
	}
	s.Attach(name, 42, "example", strings.NewReader("rejected\n"), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if s.WaitContext(ctx, name) {
		t.Fatal("pending failed flush reported success")
	}
	if got := s.CaptureStats(name); got.DroppedRecords != 1 || got.DroppedBytes != uint64(len("rejected")) {
		t.Fatalf("accepted pending record was counted as lost: %+v", got)
	}
	w.fail.Store(false)
	// Read the file directly: Query/Wait would themselves trigger a flush.
	// Recovery must happen even when the workload produces no further output.
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(s.path(name))
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
		if len(lines) == 2 {
			if entry, ok := decodeRecord(lines[1]); ok && entry.Message == message {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("pending record did not recover without a new write")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.append(Entry{Unit: name, Message: "after"}); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Read(name)
	if err != nil || len(entries) != 3 || entries[0].Message != "before" || entries[1].Message != message || entries[2].Message != "after" {
		t.Fatalf("recovery lost, duplicated, or joined records: %+v, %v", entries, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("close after recovery", err)
	}
}

func TestFailedCloseCountsEveryPendingRecord(t *testing.T) {
	s := testStore(t)
	const name = "pending.service"
	if err := s.append(Entry{Unit: name, Message: "persisted"}); err != nil {
		t.Fatal(err)
	}
	if err := s.syncUnit(name); err != nil {
		t.Fatal(err)
	}
	u := s.fileExisting(name)
	u.mu.Lock()
	u.w = &recordBuffer{writer: &partialFailureWriter{file: u.f, remaining: 37, err: syscall.ENOSPC}}
	u.mu.Unlock()
	for _, message := range []string{"first pending", "second pending"} {
		if err := s.append(Entry{Unit: name, Message: message}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err == nil {
		t.Fatal("failed flush closed successfully")
	}
	stats := s.CaptureStats(name)
	if stats.DroppedRecords != 2 || stats.DroppedBytes != uint64(len("first pending")+len("second pending")) {
		t.Fatalf("pending data loss not counted: %+v", stats)
	}
}

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
			u.w = &recordBuffer{writer: &partialFailureWriter{file: u.f, remaining: 37, err: failure}}
			u.mu.Unlock()
			s.Attach(name, 42, "example-invocation", strings.NewReader(strings.Repeat("x", 8192)+"\nafter failure\n"), nil)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if s.WaitContext(ctx, name) {
				t.Fatal("partial write failure reported successful persistence")
			}
			stats := s.CaptureStats(name)
			if stats.DroppedRecords != 1 || stats.StorageErrors == 0 {
				t.Fatalf("unreported capture loss: %+v", stats)
			}
			if err := s.Close(); err == nil {
				t.Fatal("failed buffered writer closed successfully")
			}
			if s.CaptureStats(name).DroppedRecords != 2 {
				t.Fatal("close did not report loss of the retained partial record")
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
