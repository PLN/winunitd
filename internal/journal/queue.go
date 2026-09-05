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
	done chan struct{}
	err  error
}

// Coalesce concurrent syncs for a unit and bound blocked sync workers globally.
func (s *Store) syncUnitContext(ctx context.Context, unit string) bool {
	unit = canonicalUnit(unit)
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
			pending = &journalSync{done: make(chan struct{})}
			s.syncPending[unit] = pending
			go func(p *journalSync) {
				p.err = s.syncUnit(unit)
				s.storageError(unit, p.err)
				s.queueMu.Lock()
				delete(s.syncPending, unit)
				s.queueMu.Unlock()
				close(p.done)
				<-s.syncSlots
			}(pending)
		}
		s.queueMu.Unlock()
	}
	select {
	case <-pending.done:
		return pending.err == nil
	case <-ctx.Done():
		return false
	}
}

// CaptureStats counts fragments rejected by queue admission or a write error.
// Bytes count normalized UTF-8 message bytes, excluding line endings/metadata.
// Counters are cumulative for this store lifetime across both unit streams.
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
	stats := s.dropped[unit]
	stats.StorageErrors++
	stats.LastStorageError = err.Error()
	s.dropped[unit] = stats
}

func (s *Store) CaptureStats(unit string) CaptureStats {
	if s == nil {
		return CaptureStats{}
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return s.dropped[canonicalUnit(unit)]
}

func (s *Store) enqueue(e Entry, group *captureGroup) {
	n := int64(len(e.Message))
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.queueClosed || s.queuedBytes+n > captureQueueBytes || group.bytes.Load()+n > invocationQueueBytes || group.records.Load() >= invocationQueueRecords || len(s.writeQueue) == cap(s.writeQueue) {
		stats := s.dropped[e.Unit]
		stats.DroppedRecords++
		stats.DroppedBytes += uint64(n)
		s.dropped[e.Unit] = stats
		return
	}
	s.queuedBytes += n
	group.bytes.Add(n)
	group.records.Add(1)
	group.wg.Add(1)
	s.writeQueue <- captureWrite{entry: e, group: group}
}

func (s *Store) writeCaptures() {
	defer close(s.writerDone)
	for work := range s.writeQueue {
		err := s.append(work.entry)
		s.storageError(work.entry.Unit, err)
		n := int64(len(work.entry.Message))
		s.queueMu.Lock()
		if err != nil {
			stats := s.dropped[work.entry.Unit]
			stats.DroppedRecords++
			stats.DroppedBytes += uint64(n)
			s.dropped[work.entry.Unit] = stats
		}
		s.queuedBytes -= n
		s.queueMu.Unlock()
		work.group.bytes.Add(-n)
		work.group.records.Add(-1)
		work.group.wg.Done()
	}
	s.writerErr = s.closeFiles()
}
