//go:build windows

package pathwatch

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsOpenWatchFiresOnDirModify(t *testing.T) {
	dir := t.TempDir()
	s, err := Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWatch(s)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
	case <-time.After(3 * time.Second):
		t.Fatal("directory watch did not fire after write")
	}
}

func TestWindowsOpenWatchFileFilter(t *testing.T) {
	dir := t.TempDir()
	watched := filepath.Join(dir, "watched.txt")
	other := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(watched, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Parse(watched)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWatch(s)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := os.WriteFile(other, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
		t.Fatal("sibling write must not fire a file-path watch")
	case <-time.After(400 * time.Millisecond):
	}

	if err := os.WriteFile(watched, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
	case <-time.After(3 * time.Second):
		t.Fatal("file watch did not fire after write")
	}
}

func TestWindowsOpenWatchMissingPath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-path")
	s, err := Parse(missing)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWatch(s)
	if err == nil || w != nil {
		if w != nil {
			_ = w.Close()
		}
		t.Fatal("missing path must fail")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v", err)
	}
}

func TestWindowsExists(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	s, err := ParseExists(missing)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Exists(s)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("missing path must not exist")
	}
	if err := os.WriteFile(missing, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err = Exists(s)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("created path must exist")
	}
}

func TestWindowsOpenExistsWatchCreateToSatisfy(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ready.flag")
	s, err := ParseExists(target)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenExistsWatch(s)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := os.WriteFile(target, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
	case <-time.After(3 * time.Second):
		t.Fatal("PathExists watch did not fire after create")
	}
}

func TestWindowsResolveExistsWatchMissingFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ready.flag")
	watchDir, filter, err := resolveExistsWatch(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(watchDir, dir) {
		t.Fatalf("watchDir = %q, want %q", watchDir, dir)
	}
	if !strings.EqualFold(filter, "ready.flag") {
		t.Fatalf("filter = %q", filter)
	}
}

func TestWindowsResolveExistsWatchWalksUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "ready.flag")
	watchDir, filter, err := resolveExistsWatch(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(watchDir, dir) {
		t.Fatalf("watchDir = %q, want %q", watchDir, dir)
	}
	if !strings.EqualFold(filter, "sub") {
		t.Fatalf("filter = %q, want sub", filter)
	}
}

func TestWindowsResolveExistsWatchExistingFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ready.flag")
	if err := os.WriteFile(target, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	watchDir, filter, err := resolveExistsWatch(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(watchDir, dir) {
		t.Fatalf("watchDir = %q, want %q", watchDir, dir)
	}
	if !strings.EqualFold(filter, "ready.flag") {
		t.Fatalf("filter = %q", filter)
	}
}

func TestExistsNotifyFilterNameOnly(t *testing.T) {
	want := uint32(windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME)
	if existsNotifyFilter != want {
		t.Fatalf("existsNotifyFilter = %#x, want %#x", existsNotifyFilter, want)
	}
	banned := windows.FILE_NOTIFY_CHANGE_ATTRIBUTES |
		windows.FILE_NOTIFY_CHANGE_SIZE |
		windows.FILE_NOTIFY_CHANGE_LAST_WRITE |
		windows.FILE_NOTIFY_CHANGE_CREATION |
		windows.FILE_NOTIFY_CHANGE_SECURITY
	if existsNotifyFilter&banned != 0 {
		t.Fatalf("existsNotifyFilter %#x includes content/attr/security bits", existsNotifyFilter)
	}
}

func withStatCount(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	testStatHook = func() { n.Add(1) }
	t.Cleanup(func() { testStatHook = nil })
	return &n
}

func drainExists(t *testing.T, w Watch) {
	t.Helper()
	select {
	case <-w.C():
	default:
	}
}

func TestWindowsOpenExistsWatchSiblingAppendDoesNotFire(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ready.flag")
	sibling := filepath.Join(dir, "noise.log")
	if err := os.WriteFile(sibling, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ParseExists(target)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenExistsWatch(s)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	drainExists(t, w)

	n := withStatCount(t)
	for i := 0; i < 100; i++ {
		if err := os.WriteFile(sibling, []byte{byte(i)}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-w.C():
		t.Fatal("sibling appends must not fire a PathExists watch")
	case <-time.After(400 * time.Millisecond):
	}
	if got := n.Load(); got != 0 {
		t.Fatalf("sibling appends caused %d stats, want 0", got)
	}

	if err := os.WriteFile(target, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
	case <-time.After(3 * time.Second):
		t.Fatal("PathExists watch did not fire after create")
	}
	if got := n.Load(); got > 1 {
		t.Fatalf("stats after relevant create = %d, want ≤1", got)
	}
}

func TestWindowsOpenExistsWatchSiblingCreateDoesNotFire(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ready.flag")
	s, err := ParseExists(target)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenExistsWatch(s)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	drainExists(t, w)

	n := withStatCount(t)
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
		t.Fatal("sibling create must not fire a PathExists watch")
	case <-time.After(400 * time.Millisecond):
	}
	if got := n.Load(); got != 0 {
		t.Fatalf("sibling create caused %d stats, want 0", got)
	}

	if err := os.WriteFile(target, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
	case <-time.After(3 * time.Second):
		t.Fatal("PathExists watch did not fire after matching create")
	}
	if got := n.Load(); got > 1 {
		t.Fatalf("stats after relevant create = %d, want ≤1", got)
	}
}

func TestWindowsOpenExistsWatchAncestorIgnoresSibling(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "ready.flag")
	s, err := ParseExists(target)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenExistsWatch(s)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	drainExists(t, w)

	n := withStatCount(t)
	if err := os.WriteFile(filepath.Join(dir, "noise.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
		t.Fatal("sibling create under ancestor must not fire")
	case <-time.After(400 * time.Millisecond):
	}
	if got := n.Load(); got != 0 {
		t.Fatalf("sibling noise caused %d stats, want 0", got)
	}

	before := n.Load()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
		t.Fatal("intermediate directory create must not fire until the target exists")
	case <-time.After(400 * time.Millisecond):
	}
	if got := n.Load() - before; got > 1 {
		t.Fatalf("stats after intermediate create = %d, want ≤1", got)
	}

	before = n.Load()
	if err := os.WriteFile(target, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
	case <-time.After(3 * time.Second):
		t.Fatal("PathExists watch did not fire after target create under ancestor")
	}
	if got := n.Load() - before; got > 1 {
		t.Fatalf("stats after target create = %d, want ≤1", got)
	}
}

func TestDirectoryCloseRetainsProtectedHandles(t *testing.T) {
	for _, fault := range []string{"directory", "event"} {
		t.Run(fault, func(t *testing.T) {
			watch, err := openDirWatch(t.TempDir(), "")
			if err != nil {
				t.Fatal(err)
			}
			w := watch.(*winWatch)
			t.Cleanup(func() { _ = w.Close() })
			h := w.dir
			if fault == "event" {
				h = w.event
			}
			const protectFromClose = 0x2
			if err := windows.SetHandleInformation(h, protectFromClose, protectFromClose); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = windows.SetHandleInformation(h, protectFromClose, 0) })
			if err := w.Close(); err == nil {
				t.Fatal("protected close reported success")
			}
			select {
			case <-w.done:
			default:
				t.Fatal("close returned before pending directory read finished")
			}
			retained := w.dir
			if fault == "event" {
				retained = w.event
			}
			if retained != h {
				t.Fatal("failed close discarded handle")
			}
			if err := windows.SetHandleInformation(h, protectFromClose, 0); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal("close retry", err)
			}
			if w.dir != 0 || w.event != 0 {
				t.Fatal("retry retained handles")
			}
		})
	}
}

func TestDirectoryConcurrentCloseWhileRearming(t *testing.T) {
	dir := t.TempDir()
	watch, err := openDirWatch(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	w := watch.(*winWatch)
	defer w.Close()
	stop, written := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(written)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.WriteFile(filepath.Join(dir, "noise.txt"), []byte("change"), 0600)
		}
	}()
	defer func() { close(stop); <-written }()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- w.Close() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-w.done:
	default:
		t.Fatal("directory read still running")
	}
	if w.dir != 0 || w.event != 0 {
		t.Fatal("concurrent close left handles")
	}
}
