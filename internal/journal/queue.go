package journal

import (
	"context"
	"sync"
	"sync/atomic"
)

const captureQueueRecords = 16384
const invocationQueueRecords = 12288
const captureQueueBytes = 16 << 20
const invocationQueueBytes = 4 << 20

type captureGroup struct {
	wg      sync.WaitGroup
	bytes   atomic.Int64
	records atomic.Int64
}

type captureWrite struct {
	entry Entry
	group *captureGroup
}

type journalSync struct {
	done       chan struct{}
	err        error
	generation uint64
}

// Coalesce concurrent syncs for a unit and bound blocked sync workers globally.
func (s *Store) syncUnitContext(ctx context.Context, unit string) bool {
	unit = canonicalUnit(unit)
	wanted := s.writeSequence.Load()
	for {
		s.queueMu.Lock()
		pending := s.syncPending[unit]
		s.queueMu.Unlock()
		if pending == nil {
			select {
			case s.syncSlots <- struct{}{}:
			case <-ctx.Done():
				return false
			}
			s.queueMu.Lock()
			pending = s.syncPending[unit]
			if pending != nil {
				<-s.syncSlots
			} else {
				pending = &journalSync{done: make(chan struct{}), generation: s.writeSequence.Load()}
				s.syncPending[unit] = pending
				go func(p *journalSync) {
					p.err = s.syncAndRetireUnit(unit)
					s.storageError(unit, p.err)
					if s.onSynced != nil {
						s.onSynced()
					}
					s.queueMu.Lock()
					delete(s.syncPending, unit)
					s.queueMu.Unlock()
					close(p.done)
					<-s.syncSlots
				}(pending)
			}
			s.queueMu.Unlock()
		}
		if pending.generation < wanted && s.onSyncJoin != nil {
			s.onSyncJoin()
		}
		select {
		case <-pending.done:
			if pending.err != nil {
				return false
			}
			if pending.generation >= wanted || s.fileExisting(unit) == nil {
				return true
			}
			// A new capture may have written after the shared sync began. Join it
			// for ownership, then start a sync that covers this caller's writes.
		case <-ctx.Done():
			return false
		}
	}
}

// CaptureStats counts fragments rejected by queue admission or a write error.
// Bytes count normalized UTF-8 message bytes, excluding line endings/metadata.
// Counters cover both streams. Loaded names are retained by RetainNames; other
// names share bounded history. TotalCaptureStats always covers the store lifetime.
// Unwritten records retained after a flush failure are counted only if close
// abandons them. Sync failures and external file damage can lose additional data.
type CaptureStats struct {
	DroppedRecords   uint64
	DroppedBytes     uint64
	StorageErrors    uint64
	LastStorageError string
}

func (s *Store) storageError(unit string, err error) {
	if err == nil {
		return
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	s.addStatsLocked(unit, CaptureStats{StorageErrors: 1, LastStorageError: err.Error()})
}

func (s *Store) CaptureStats(unit string) CaptureStats {
	if s == nil {
		return CaptureStats{}
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return s.dropped[canonicalUnit(unit)]
}
