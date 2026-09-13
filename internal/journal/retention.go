package journal

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	maxUnitFiles         = 1024
	maxStatsNames        = 2048
	maxStorageErrorBytes = 4096
)

// MaxRetainedNames bounds the loaded/owned names protected from counter eviction.
const MaxRetainedNames = 1024

var (
	errFileCapacity = errors.New("journal file capacity exhausted; flush completed captures and retry")
	errFileRetired  = errors.New("journal file retired")
)

// syncAndRetireUnit releases a successfully persisted file without discarding
// failed writes or close ownership. A writer that selected the old record but
// has not acquired its lock retries against the current record. No store lock
// is held during I/O. Active auxiliary captures may reopen the same file.
func (s *Store) syncAndRetireUnit(name string) error {
	u := s.fileExisting(name)
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed || u.retired {
		return nil
	}
	if err := u.syncLocked(); err != nil {
		return err
	}
	if u.f != nil {
		if s.onClose != nil {
			if err := s.onClose(); err != nil {
				return err
			}
		}
		if err := u.f.Close(); err != nil {
			return err
		}
	}
	if u.timer != nil {
		u.timer.Stop()
		u.timer = nil
	}
	u.f, u.w = nil, nil
	u.retired = true
	s.mu.Lock()
	if s.files[u.unit] == u {
		delete(s.files, u.unit)
	}
	s.mu.Unlock()
	return nil
}

// RetainNames protects per-name counters for loaded and retained manager units.
// It changes metadata only, performs no I/O and accepts at most 1024 names.
// Other names share a bounded history; eviction never reduces lifetime totals.
func (s *Store) RetainNames(names []string) error {
	if s == nil {
		return nil
	}
	if len(names) > MaxRetainedNames {
		return errors.New("journal retention exceeds 1024 names")
	}
	next := make(map[string]bool, len(names))
	for _, name := range names {
		next[canonicalUnit(name)] = true
	}
	s.queueMu.Lock()
	s.retainedNames = next
	s.queueMu.Unlock()
	return nil
}

// TotalCaptureStats includes every name observed during this store lifetime,
// including historical names no longer retained by CaptureStats.
func (s *Store) TotalCaptureStats() CaptureStats {
	if s == nil {
		return CaptureStats{}
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return s.totalStats
}

func addStats(to *CaptureStats, delta CaptureStats) {
	to.DroppedRecords += delta.DroppedRecords
	to.DroppedBytes += delta.DroppedBytes
	to.StorageErrors += delta.StorageErrors
	if delta.LastStorageError != "" {
		to.LastStorageError = delta.LastStorageError
	}
}

// queueMu protects counters and their bounded insertion-order history. Loaded
// names are skipped during eviction. At most half the history can be pinned.
func (s *Store) addStatsLocked(name string, delta CaptureStats) {
	name = canonicalUnit(name)
	if delta.LastStorageError != "" {
		delta.LastStorageError = strings.ToValidUTF8(delta.LastStorageError, "\uFFFD")
		if len(delta.LastStorageError) > maxStorageErrorBytes {
			delta.LastStorageError = delta.LastStorageError[:maxStorageErrorBytes]
			for !utf8.ValidString(delta.LastStorageError) {
				delta.LastStorageError = delta.LastStorageError[:len(delta.LastStorageError)-1]
			}
		}
		delta.LastStorageError = strings.Clone(delta.LastStorageError)
	}
	addStats(&s.totalStats, delta)
	if _, ok := s.dropped[name]; !ok {
		if len(s.dropped) == maxStatsNames {
			for item := s.statsOrder.Front(); item != nil; item = item.Next() {
				old := item.Value.(string)
				if !s.retainedNames[old] {
					delete(s.dropped, old)
					s.statsOrder.Remove(item)
					break
				}
			}
		}
		s.statsOrder.PushBack(name)
	}
	stats := s.dropped[name]
	addStats(&stats, delta)
	s.dropped[name] = stats
}
