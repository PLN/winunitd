package journal

import (
	"context"
	"io"
)

// Capture owns the completion wait for streams attached independently of the
// unit's main invocation. The process owner must terminate/close those streams.
type Capture struct {
	store *Store
	unit  string
	done  <-chan struct{}
}

// Complete reports whether streams and queued writes have completed, without I/O.
// A separate flush may still fail and remains owned by the store.
func (c *Capture) Complete() bool {
	if c == nil {
		return true
	}
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// AttachConcurrent drains a bounded-queue auxiliary invocation into the same
// unit log without waiting for or replacing the main invocation's capture.
// The returned handle joins its streams and queued writes independently.
func (s *Store) AttachConcurrent(unit string, pid int, invocationID string, stdout, stderr io.Reader) *Capture {
	unit = canonicalUnit(unit)
	if invocationID == "" {
		invocationID = NewInvocationID()
	}
	group := &captureGroup{}
	group.wg.Add(2)
	origin := s.snapshotOrigin()
	for _, stream := range []struct {
		name   string
		reader io.Reader
	}{{"stdout", stdout}, {"stderr", stderr}} {
		go func() {
			defer group.wg.Done()
			if s == nil {
				drain(stream.reader)
				return
			}
			s.capture(unit, pid, invocationID, origin, stream.name, stream.reader, group)
		}()
	}
	done := make(chan struct{})
	go func() { group.wg.Wait(); close(done) }()
	return &Capture{store: s, unit: unit, done: done}
}

// WaitContext waits for this capture only, then requests the unit's shared
// flush. Expired callers leave the same owned wait intact; retries add no wait
// goroutines. The store retains any pending writer or sync operation.
func (c *Capture) WaitContext(ctx context.Context) bool {
	if c == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-c.done:
		if c.store == nil {
			return true
		}
		if !c.store.syncUnitContext(ctx, c.unit) {
			return false
		}
		c.store.mu.Lock()
		if c.store.mainCaptures[c.unit] == c {
			delete(c.store.mainCaptures, c.unit)
		}
		c.store.mu.Unlock()
		return true
	case <-ctx.Done():
		return false
	}
}
