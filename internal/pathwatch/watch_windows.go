//go:build windows

package pathwatch

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

const (
	notifyFilter = windows.FILE_NOTIFY_CHANGE_FILE_NAME |
		windows.FILE_NOTIFY_CHANGE_DIR_NAME |
		windows.FILE_NOTIFY_CHANGE_ATTRIBUTES |
		windows.FILE_NOTIFY_CHANGE_SIZE |
		windows.FILE_NOTIFY_CHANGE_LAST_WRITE |
		windows.FILE_NOTIFY_CHANGE_CREATION |
		windows.FILE_NOTIFY_CHANGE_SECURITY

	// existsNotifyFilter is the narrowest ReadDirectoryChangesW mask that
	// still reports create, delete, and rename of a directory entry.
	existsNotifyFilter = windows.FILE_NOTIFY_CHANGE_FILE_NAME |
		windows.FILE_NOTIFY_CHANGE_DIR_NAME

	notifyBufSize = 64 * 1024
)

type winWatch struct {
	dir    windows.Handle
	event  windows.Handle
	filter string // basename; empty means any change in the directory
	mask   uint32
	ch     chan struct{}

	mu        sync.Mutex
	closed    bool
	armedOnce sync.Once
	armed     chan struct{}
}

// OpenWatch watches spec via ReadDirectoryChangesW (non-recursive).
// A directory path watches that directory. A file path watches the parent
// directory and filters by name. A missing path returns ErrMissingPath.
func OpenWatch(s Spec) (Watch, error) {
	if s.Raw == "" {
		parsed, err := Parse(s.Raw)
		if err != nil {
			return nil, err
		}
		s = parsed
	} else if _, err := Parse(s.Raw); err != nil {
		return nil, err
	}

	watchDir, filter, err := resolveWatch(s.Raw)
	if err != nil {
		return nil, err
	}
	return openDirWatch(watchDir, filter)
}

func openDirWatch(watchDir, filter string) (Watch, error) {
	return openDirWatchNotify(watchDir, filter, notifyFilter)
}

func openDirWatchNotify(watchDir, filter string, mask uint32) (Watch, error) {
	p, err := windows.UTF16PtrFromString(watchDir)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnwatchable, watchDir)
	}
	h, err := windows.CreateFile(
		p,
		windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED,
		0,
	)
	if err != nil {
		if isMissing(err) {
			return nil, fmt.Errorf("%w: %s", ErrMissingPath, watchDir)
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrUnwatchable, watchDir, err)
	}
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("%w: %s: %v", ErrUnwatchable, watchDir, err)
	}
	if mask == 0 {
		mask = notifyFilter
	}
	armed := make(chan struct{})
	w := &winWatch{
		dir:    h,
		event:  ev,
		filter: filter,
		mask:   mask,
		ch:     make(chan struct{}, 1),
		armed:  armed,
	}
	go w.loop()
	// Return only after ReadDirectoryChangesW is pending so a create
	// immediately after OpenWatch / OpenExistsWatch cannot be missed.
	<-armed
	return w, nil
}

func (w *winWatch) C() <-chan struct{} { return w.ch }

func (w *winWatch) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()
	_ = windows.CancelIoEx(w.dir, nil)
	_ = windows.SetEvent(w.event)
	_ = windows.CloseHandle(w.dir)
	_ = windows.CloseHandle(w.event)
	return nil
}

func (w *winWatch) noteArmed() {
	w.armedOnce.Do(func() {
		if w.armed != nil {
			close(w.armed)
		}
	})
}

func (w *winWatch) loop() {
	defer close(w.ch)
	defer w.noteArmed()
	buf := make([]byte, notifyBufSize)
	for {
		if w.isClosed() {
			return
		}
		var ov windows.Overlapped
		ov.HEvent = w.event
		err := windows.ReadDirectoryChanges(
			w.dir,
			&buf[0],
			uint32(len(buf)),
			false, // non-recursive: this directory only
			w.mask,
			nil,
			&ov,
			0,
		)
		w.noteArmed()
		if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
			return
		}
		if _, waitErr := windows.WaitForSingleObject(w.event, windows.INFINITE); waitErr != nil || w.isClosed() {
			return
		}
		var n uint32
		if err := windows.GetOverlappedResult(w.dir, &ov, &n, false); err != nil {
			if w.isClosed() {
				return
			}
			if errors.Is(err, windows.ERROR_NOTIFY_ENUM_DIR) {
				w.signal()
				continue
			}
			return
		}
		if w.match(buf[:n]) {
			w.signal()
		}
	}
}

func (w *winWatch) signal() {
	if w.isClosed() {
		return
	}
	select {
	case w.ch <- struct{}{}:
	default:
	}
}

func (w *winWatch) match(buf []byte) bool {
	if w.filter == "" {
		// Directory PathChanged=: any completion is a change. Do not
		// require a parsed filename; empty or unparseable buffers still
		// mean something happened here. PathExists= ancestor watches
		// always pass a basename filter.
		return true
	}
	if len(buf) == 0 {
		return false
	}
	for _, name := range notifyNames(buf) {
		if nameMatches(name, w.filter) {
			return true
		}
	}
	return false
}

func (w *winWatch) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func resolveWatch(raw string) (dir, filter string, err error) {
	p, err := windows.UTF16PtrFromString(raw)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s", ErrUnwatchable, raw)
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		if isMissing(err) {
			return "", "", fmt.Errorf("%w: %s", ErrMissingPath, raw)
		}
		return "", "", fmt.Errorf("%w: %s: %v", ErrUnwatchable, raw, err)
	}
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		dir := strings.TrimRight(raw, `/\`)
		if len(dir) == 2 && dir[1] == ':' {
			dir += `\`
		}
		return dir, "", nil
	}
	dir, name := SplitDirName(raw)
	if dir == "" || name == "" {
		return "", "", fmt.Errorf("%w: %s", ErrUnwatchable, raw)
	}
	return dir, name, nil
}

func notifyNames(buf []byte) []string {
	var names []string
	for len(buf) >= 12 {
		next := binary.LittleEndian.Uint32(buf[0:4])
		nameLen := binary.LittleEndian.Uint32(buf[8:12])
		end := 12 + int(nameLen)
		if nameLen == 0 || end > len(buf) || nameLen%2 != 0 {
			break
		}
		n := int(nameLen / 2)
		u16 := make([]uint16, n)
		for i := 0; i < n; i++ {
			u16[i] = binary.LittleEndian.Uint16(buf[12+i*2:])
		}
		names = append(names, windows.UTF16ToString(u16))
		if next == 0 {
			break
		}
		if int(next) > len(buf) {
			break
		}
		buf = buf[next:]
	}
	return names
}

func nameMatches(got, want string) bool {
	if i := strings.LastIndexAny(got, `/\`); i >= 0 {
		got = got[i+1:]
	}
	return strings.EqualFold(got, want)
}

func isMissing(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_PATH_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_INVALID_NAME)
}
