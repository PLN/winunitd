package journal

import "container/list"

// Covers the manager's unit and stop-helper admission limits, with a separate
// bound for standalone callers. A writer can additionally own one popped item.
const maxQueuedCaptureGroups = 2048

type capturePending struct {
	group *captureGroup
	items list.List
	bytes int64
	turn  *list.Element
}

func (s *Store) wakeCaptureWriterLocked() {
	select {
	case s.captureWake <- struct{}{}:
	default:
	}
}

func (s *Store) dropCaptureLocked(work captureWrite) {
	stats := s.dropped[work.entry.Unit]
	stats.DroppedRecords++
	stats.DroppedBytes += uint64(len(work.entry.Message))
	s.dropped[work.entry.Unit] = stats
}

func (s *Store) releaseCaptureLocked(work captureWrite) {
	n := int64(len(work.entry.Message))
	s.queuedBytes -= n
	s.queuedRecords--
	work.group.bytes.Add(-n)
	work.group.records.Add(-1)
	work.group.wg.Done()
}

func (s *Store) removeCaptureQueueLocked(q *capturePending) {
	delete(s.captureQueues, q.group)
	s.captureOrder.Remove(q.turn)
}

// Under aggregate pressure, a less represented invocation may displace the
// newest pending record of a larger queue. Preserve at least one pending record
// per victim and never evict an in-flight write. Remaining records keep order.
func (s *Store) makeCaptureRoomLocked(group *captureGroup, size int64) bool {
	for s.queuedBytes+size > captureQueueBytes || s.queuedRecords >= captureQueueRecords {
		bytePressure := s.queuedBytes+size > captureQueueBytes
		var victim *capturePending
		for _, q := range s.captureQueues {
			if q.group == group || q.items.Len() <= 1 {
				continue
			}
			if bytePressure {
				if q.bytes <= group.bytes.Load()+size {
					continue
				}
				if victim == nil || q.bytes > victim.bytes {
					victim = q
				}
			} else {
				if int64(q.items.Len()) <= group.records.Load()+1 {
					continue
				}
				if victim == nil || q.items.Len() > victim.items.Len() {
					victim = q
				}
			}
		}
		if victim == nil {
			return false
		}
		last := victim.items.Back()
		work := last.Value.(captureWrite)
		victim.items.Remove(last)
		victim.bytes -= int64(len(work.entry.Message))
		s.dropCaptureLocked(work)
		s.releaseCaptureLocked(work)
	}
	return true
}

func (s *Store) enqueue(e Entry, group *captureGroup) {
	work := captureWrite{entry: e, group: group}
	n := int64(len(e.Message))
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	q := s.captureQueues[group]
	if s.queueClosed || n > captureQueueBytes || group.bytes.Load()+n > invocationQueueBytes || group.records.Load() >= invocationQueueRecords || (q == nil && len(s.captureQueues) >= maxQueuedCaptureGroups) {
		s.dropCaptureLocked(work)
		return
	}
	if !s.makeCaptureRoomLocked(group, n) {
		s.dropCaptureLocked(work)
		return
	}
	if q == nil {
		q = &capturePending{group: group}
		q.turn = s.captureOrder.PushBack(q)
		s.captureQueues[group] = q
	}
	q.items.PushBack(work)
	q.bytes += n
	s.queuedBytes += n
	s.queuedRecords++
	group.bytes.Add(n)
	group.records.Add(1)
	group.wg.Add(1)
	s.wakeCaptureWriterLocked()
}

func (s *Store) nextCapture() (captureWrite, bool) {
	for {
		s.queueMu.Lock()
		front := s.captureOrder.Front()
		if front != nil {
			q := front.Value.(*capturePending)
			item := q.items.Front()
			work := item.Value.(captureWrite)
			q.items.Remove(item)
			q.bytes -= int64(len(work.entry.Message))
			if q.items.Len() == 0 {
				s.removeCaptureQueueLocked(q)
			} else {
				s.captureOrder.MoveToBack(front)
			}
			s.queueMu.Unlock()
			return work, true
		}
		closed := s.queueClosed
		s.queueMu.Unlock()
		if closed {
			return captureWrite{}, false
		}
		<-s.captureWake
	}
}

func (s *Store) writeCaptures() {
	defer close(s.writerDone)
	for {
		work, ok := s.nextCapture()
		if !ok {
			break
		}
		err := s.append(work.entry)
		s.storageError(work.entry.Unit, err)
		s.queueMu.Lock()
		if err != nil {
			s.dropCaptureLocked(work)
		}
		s.releaseCaptureLocked(work)
		s.queueMu.Unlock()
	}
	s.writerErr = s.closeFiles()
}
