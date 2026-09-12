package journal

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
