package journal

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// This opt-in test fills only a separately prepared, tiny disposable volume.
// Ordinary test runs skip it; a skip is not disk-pressure qualification.
func TestDisposableVolumeDiskFullRecovery(t *testing.T) {
	root := os.Getenv("WINUNITD_TEST_JOURNAL_VOLUME")
	if root == "" {
		t.Skip("requires an explicitly prepared disposable volume")
	}
	root = filepath.Clean(root)
	if len(root) != 3 || root[1:] != `:\` || strings.EqualFold(filepath.VolumeName(root), os.Getenv("SystemDrive")) {
		t.Fatal("fixture must be a separate drive root")
	}
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	var label [256]uint16
	if err := windows.GetVolumeInformation(p, &label[0], uint32(len(label)), nil, nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &available, &total, &free); err != nil {
		t.Fatal(err)
	}
	if windows.UTF16ToString(label[:]) != "winunitd-test" || total < 16<<20 || total > 128<<20 {
		t.Fatal("fixture requires a 16-128 MiB volume labeled winunitd-test")
	}
	dir, err := os.MkdirTemp(root, "winunitd-diskfull-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	const name = "pressure.service"
	if err := s.append(Entry{Unit: name, Message: "before"}); err != nil {
		t.Fatal(err)
	}
	if err := s.syncUnit(name); err != nil {
		t.Fatal(err)
	}
	filler, err := os.OpenFile(filepath.Join(dir, "filler"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filler.Close(); _ = os.Remove(filler.Name()) })
	isFull := func(err error) bool {
		return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL)
	}
	chunk := bytes.Repeat([]byte{0xa5}, 4<<10)
	var written uint64
	for {
		n, err := filler.Write(chunk)
		written += uint64(n)
		if isFull(err) {
			break
		}
		if err != nil || n != len(chunk) || written > total {
			t.Fatalf("unexpected filler result: bytes=%d error=%v", written, err)
		}
	}
	message := strings.Repeat("x", 64<<10)
	if err := s.append(Entry{Unit: name, Message: message}); err != nil {
		t.Fatal(err)
	}
	if err := s.syncUnit(name); !isFull(err) {
		t.Fatalf("expected native disk-full error, got %v", err)
	}
	s.Attach(name, 42, "example", strings.NewReader("rejected\n"), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if s.WaitContext(ctx, name) {
		t.Fatal("disk-full capture reported persistence")
	}
	if stats := s.CaptureStats(name); stats.DroppedRecords != 1 || stats.StorageErrors == 0 {
		t.Fatalf("missing pressure accounting: %+v", stats)
	}
	if err := filler.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filler.Name()); err != nil {
		t.Fatal(err)
	}
	// Raw reads cannot trigger a flush: recovery must happen without new output.
	deadline := time.Now().Add(45 * time.Second)
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
			t.Fatal("journal did not recover after freeing the disposable volume")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := s.append(Entry{Unit: name, Message: "after"}); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Read(name)
	if err != nil || len(entries) != 3 || entries[0].Message != "before" || entries[1].Message != message || entries[2].Message != "after" {
		t.Fatalf("recovery lost or duplicated records: count=%d error=%v", len(entries), err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("native disk-full and automatic recovery passed; volume bytes=%d filler bytes=%d", total, written)
}
