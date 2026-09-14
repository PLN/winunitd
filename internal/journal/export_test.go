package journal

import (
	"io"
	"sync"
)

// OpenOperationalPressureTestStore prepares independently stalled readers.
// ArmOperationalPressureFault adds the write fault after manager admission.
func OpenOperationalPressureTestStore(dir string, onScan func()) (*Store, error) {
	s, err := Open(dir)
	if err != nil {
		return nil, err
	}
	if err = s.append(Entry{Unit: "reader.service", Message: "before fault"}); err == nil {
		err = s.syncUnit("reader.service")
	}
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	s.onScan = onScan
	return s, nil
}

func (s *Store) ArmOperationalPressureFault(onWrite func(), nativeDisk bool) (func(), error) {
	const name = "pressure-0.service"
	if err := s.append(Entry{Unit: name, Message: "before fault"}); err != nil {
		return nil, err
	}
	if err := s.syncUnit(name); err != nil {
		return nil, err
	}
	u := s.fileExisting(name)
	u.mu.Lock()
	if nativeDisk {
		u.w = &recordBuffer{writer: &pressureWriteGate{writer: u.f, enter: onWrite}}
		u.mu.Unlock()
		return func() {}, nil
	}
	w := &recoveringWriter{file: u.f, remaining: 37}
	w.fail.Store(true)
	u.w = &recordBuffer{writer: &pressureWriteGate{writer: w, enter: onWrite}}
	u.mu.Unlock()
	return func() { w.fail.Store(false) }, nil
}

type pressureWriteGate struct {
	writer io.Writer
	enter  func()
	once   sync.Once
}

func (w *pressureWriteGate) Write(p []byte) (int, error) {
	w.once.Do(w.enter)
	return w.writer.Write(p)
}

func OpenRetentionTestStore(dir string, maxFiles int) (*Store, error) {
	s, err := Open(dir)
	if err == nil {
		s.maxDiskFiles = maxFiles
	}
	return s, err
}

// Exported only in the journal test build, for external manager integration.
func OpenPressureTestStore(dir string, onOpen func()) (*Store, error) {
	s, err := Open(dir)
	if err == nil {
		s.onOpen = onOpen
	}
	return s, err
}

type PressureTestQueue struct {
	Bytes   int64
	Records int
	Groups  int
	ByUnit  map[string]int64
}

func (s *Store) PressureTestQueue() PressureTestQueue {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	result := PressureTestQueue{Bytes: s.queuedBytes, Records: s.queuedRecords, Groups: len(s.captureQueues), ByUnit: make(map[string]int64)}
	for _, q := range s.captureQueues {
		name := q.items.Front().Value.(captureWrite).entry.Unit
		result.ByUnit[name] = q.group.bytes.Load()
	}
	return result
}

func (s *Store) RetentionTestCounts() (files, names int) {
	s.mu.Lock()
	files = len(s.files)
	s.mu.Unlock()
	s.queueMu.Lock()
	names = len(s.dropped)
	s.queueMu.Unlock()
	return
}
