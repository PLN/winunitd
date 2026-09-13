package journal

import (
	"bufio"
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultMaxSize is the current-file cap before rotate (DESIGN.md §22).
const DefaultMaxSize = 10 << 20 // 10 MiB

// DefaultKeep is the number of rotated archives retained per unit
// (foo.log.1 … foo.log.3).
const DefaultKeep = 3

// DefaultFlushEvery is the timer-flush interval. Sync is not used here.
const DefaultFlushEvery = 100 * time.Millisecond

// followWait is how long Logs Follow=true waits for a new line.
const followWait = time.Second

const followPoll = 50 * time.Millisecond

// Store is a directory of per-unit append-only journal files.
type Store struct {
	dir        string
	maxSize    int64
	keep       int
	flushEvery time.Duration

	mu            sync.Mutex
	closed        bool
	files         map[string]*unitFile
	mainCaptures  map[string]*Capture
	origin        Origin
	queueMu       sync.Mutex
	captureWake   chan struct{}
	captureQueues map[*captureGroup]*capturePending
	captureOrder  list.List
	queuedRecords int
	writerDone    chan struct{}
	writerErr     error
	queueClosed   bool
	queuedBytes   int64
	dropped       map[string]CaptureStats
	statsOrder    list.List
	retainedNames map[string]bool
	totalStats    CaptureStats
	writeSequence atomic.Uint64
	syncPending   map[string]*journalSync
	syncSlots     chan struct{}
	querySlots    chan struct{}

	// onOpen / onSync / onScan / onEntryID are test hooks (nil in production).
	// onOpen fires after a successful OpenFile; onSync fires immediately
	// before Sync; onScan fires once per decoded line during the unlocked
	// scan (write lock must not be held); onEntryID fires when a line's
	// cursor id is computed.
	// onClose injects retirement failure before native Close; onSynced pauses
	// completed sync publication; onSyncJoin observes a generation-stale join.
	onOpen     func()
	onSync     func()
	onScan     func()
	onEntryID  func()
	onClose    func() error
	onSynced   func()
	onSyncJoin func()
}

// Entry is one journal fragment. v=2 adds Severity, Session, and UserSID.
// v=3 adds Continuation (follows a fragment on the same invocation/stream)
// and Partial (no terminating newline). Older records default these to false.
type Entry struct {
	Timestamp    time.Time
	Unit         string
	PID          int
	Stream       string
	Message      string
	InvocationID string
	Severity     string
	Session      string
	UserSID      string
	Continuation bool
	Partial      bool
}

type record struct {
	V            int    `json:"v"`
	Timestamp    string `json:"timestamp"`
	Unit         string `json:"unit"`
	PID          int    `json:"pid,omitempty"`
	Stream       string `json:"stream,omitempty"`
	Message      string `json:"message"`
	InvocationID string `json:"invocationId,omitempty"`
	Severity     string `json:"severity"`
	Session      string `json:"session"`
	UserSID      string `json:"userSid"`
	Continuation bool   `json:"continuation,omitempty"`
	Partial      bool   `json:"partial,omitempty"`
}

type unitFile struct {
	mu         sync.Mutex
	unit       string
	path       string
	store      *Store
	f          *os.File
	w          *recordBuffer
	size       int64
	timer      *time.Timer
	closed     bool
	retired    bool
	writeErr   error
	retryDelay time.Duration
	retryAt    time.Time
}

// Open creates dir if needed and returns a store rooted there.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("journal directory required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:           dir,
		maxSize:       DefaultMaxSize,
		keep:          DefaultKeep,
		flushEvery:    DefaultFlushEvery,
		files:         make(map[string]*unitFile),
		mainCaptures:  make(map[string]*Capture),
		captureWake:   make(chan struct{}, 1),
		captureQueues: make(map[*captureGroup]*capturePending),
		writerDone:    make(chan struct{}),
		dropped:       make(map[string]CaptureStats),
		syncPending:   make(map[string]*journalSync),
		syncSlots:     make(chan struct{}, 4),
		querySlots:    make(chan struct{}, queryWorkers),
	}
	go s.writeCaptures()
	return s, nil
}

// Dir is the journal root (<base-dir>\journal).
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// SetOrigin records user-manager identity for subsequent Attach writes.
// Empty Origin is the system-scope default.
func (s *Store) SetOrigin(o Origin) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.origin = o
	s.mu.Unlock()
}

func (s *Store) snapshotOrigin() Origin {
	if s == nil {
		return Origin{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.origin
}

// Close flushes and Syncs every open unit file, then closes them.
// Further Append/Attach writes are dropped. Safe to call more than once.
func (s *Store) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.CloseContext(ctx)
}

// CloseContext bounds waiting for the journal writer. A timed-out writer
// retains its file ownership and finishes cleanup if storage recovers.
func (s *Store) CloseContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.queueMu.Lock()
	if !s.queueClosed {
		s.queueClosed = true
		s.wakeCaptureWriterLocked()
	}
	s.queueMu.Unlock()
	select {
	case <-s.writerDone:
		return s.writerErr
	case <-ctx.Done():
		return fmt.Errorf("journal close: %w", ctx.Err())
	}
}

func (s *Store) closeFiles() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	files := make([]*unitFile, 0, len(s.files))
	for _, f := range s.files {
		files = append(files, f)
	}
	s.mu.Unlock()

	var err error
	for _, f := range files {
		if e := f.close(); e != nil {
			s.storageError(f.unit, e)
			if err == nil {
				err = e
			}
		}
	}
	return err
}

// Attach queues stdout and stderr for the unit's journal file, tagging
// each line with invocationID (DESIGN.md §22, §24) and v=2 fields
// (severity from stream; session and user SID from SetOrigin). Empty
// invocationID is replaced with a new ID so isolated journal use still
// correlates a capture. Nil streams are ignored. A nil Store still
// drains so the child cannot block.
func (s *Store) Attach(unit string, pid int, invocationID string, stdout, stderr io.Reader) *Capture {
	unit = canonicalUnit(unit)
	if s == nil {
		return s.AttachConcurrent(unit, pid, invocationID, stdout, stderr)
	}
	if invocationID == "" {
		invocationID = NewInvocationID()
	}
	origin := s.snapshotOrigin()
	group := &captureGroup{}
	group.wg.Add(2)
	done := make(chan struct{})
	capture := &Capture{store: s, unit: unit, done: done}
	s.mu.Lock()
	previous := s.mainCaptures[unit]
	s.mainCaptures[unit] = capture
	s.mu.Unlock()
	// Reserve this invocation before waiting, so concurrent Attach calls form
	// a chain and Wait always observes the newest accepted main capture.
	go func() {
		group.wg.Wait()
		close(done)
		s.mu.Lock()
		if s.mainCaptures[unit] == capture {
			delete(s.mainCaptures, unit)
		}
		s.mu.Unlock()
	}()
	if previous != nil {
		<-previous.done
	}
	for _, stream := range []struct {
		name   string
		reader io.Reader
	}{{"stdout", stdout}, {"stderr", stderr}} {
		go func() {
			defer group.wg.Done()
			s.capture(unit, pid, invocationID, origin, stream.name, stream.reader, group)
		}()
	}
	return capture
}

// Wait joins the current main capture and flushes its unit journal.
func (s *Store) Wait(unit string) { s.WaitContext(context.Background(), unit) }

// WaitContext preserves capture ownership on timeout. Every retry joins the
// same completion; it cannot report success merely because an earlier wait expired.
func (s *Store) WaitContext(ctx context.Context, unit string) bool {
	if s == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	unit = canonicalUnit(unit)
	s.mu.Lock()
	capture := s.mainCaptures[unit]
	s.mu.Unlock()
	if capture == nil {
		return s.syncUnitContext(ctx, unit)
	}
	return capture.WaitContext(ctx)
}

func (s *Store) capture(unit string, pid int, inv string, origin Origin, stream string, r io.Reader, group *captureGroup) {
	if r == nil {
		return
	}
	captureFragments(r, func(msg string, continuation, partial bool) {
		s.enqueue(Entry{
			Timestamp:    time.Now().UTC(),
			Unit:         unit,
			PID:          pid,
			Stream:       stream,
			Message:      msg,
			InvocationID: inv,
			Severity:     SeverityFromStream(stream),
			Session:      origin.Session,
			UserSID:      origin.UserSID,
			Continuation: continuation,
			Partial:      partial,
		}, group)
	})
}

func (s *Store) append(e Entry) error {
	if s == nil || e.Unit == "" {
		return nil
	}
	e.Unit = canonicalUnit(e.Unit)
	if e.Severity == "" {
		e.Severity = SeverityFromStream(e.Stream)
	}
	rec := record{
		V:            FormatVersion,
		Timestamp:    e.Timestamp.UTC().Format(time.RFC3339Nano),
		Unit:         e.Unit,
		PID:          e.PID,
		Stream:       e.Stream,
		Message:      e.Message,
		InvocationID: e.InvocationID,
		Severity:     e.Severity,
		Session:      e.Session,
		UserSID:      e.UserSID,
		Continuation: e.Continuation,
		Partial:      e.Partial,
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	for {
		f, err := s.file(e.Unit)
		if err != nil {
			return err
		}
		err = f.write(raw, len(e.Message))
		if !errors.Is(err, errFileRetired) {
			return err
		}
	}
}

func (s *Store) file(unit string) (*unitFile, error) {
	unit = canonicalUnit(unit)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("journal closed")
	}
	if s.files == nil {
		s.files = make(map[string]*unitFile)
	}
	f := s.files[unit]
	if f == nil {
		if len(s.files) >= maxUnitFiles {
			return nil, errFileCapacity
		}
		f = &unitFile{
			unit:  unit,
			path:  s.path(unit),
			store: s,
		}
		s.files[unit] = f
	}
	return f, nil
}

func (u *unitFile) write(raw []byte, messageBytes int) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.retired {
		return errFileRetired
	}
	if u.closed {
		return fmt.Errorf("journal file closed")
	}
	if u.f == nil {
		if err := u.openLocked(); err != nil {
			return err
		}
	}
	if u.writeErr != nil {
		if time.Now().Before(u.retryAt) {
			return u.writeErr
		}
		if err := u.flushLocked(); err != nil {
			return err
		}
	}
	max := u.store.maxSize
	if max <= 0 {
		max = DefaultMaxSize
	}
	if u.size > 0 && u.size+int64(len(raw)) > max {
		if err := u.rotateLocked(); err != nil {
			return err
		}
	}
	if err := u.w.append(raw, messageBytes); err != nil {
		u.writeFailureLocked(err)
		return err
	}
	u.size += int64(len(raw))
	u.store.writeSequence.Add(1)
	u.scheduleFlushLocked()
	return nil
}

func (u *unitFile) openLocked() error {
	f, err := os.OpenFile(u.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	size, repaired, err := repairJournalTail(f)
	if err != nil {
		_ = f.Close()
		return err
	}
	if repaired {
		u.store.storageError(u.unit, fmt.Errorf("recovered journal record boundary after interrupted write"))
	}
	u.f = f
	u.w = &recordBuffer{writer: f}
	u.size = size
	if u.store.onOpen != nil {
		u.store.onOpen()
	}
	return nil
}

func (u *unitFile) scheduleFlushLocked() {
	if u.timer != nil || u.closed {
		return
	}
	every := u.store.flushEvery
	if every <= 0 {
		every = DefaultFlushEvery
	}
	if retry := time.Until(u.retryAt); retry > every {
		every = retry
	}
	var scheduled *time.Timer
	scheduled = time.AfterFunc(every, func() {
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.timer != scheduled {
			return
		}
		u.timer = nil
		if u.closed {
			return
		}
		u.store.storageError(u.unit, u.flushLocked())
	})
	u.timer = scheduled
}

func (u *unitFile) flushLocked() error {
	if u.w == nil {
		return nil
	}
	err := u.w.Flush()
	if err != nil {
		u.writeFailureLocked(err)
	} else {
		u.writeErr = nil
		u.retryDelay = 0
		u.retryAt = time.Time{}
	}
	return err
}

func (u *unitFile) writeFailureLocked(err error) {
	u.writeErr = err
	u.retryDelay = min(max(time.Second, u.retryDelay*2), 30*time.Second)
	u.retryAt = time.Now().Add(u.retryDelay)
	if u.timer != nil {
		u.timer.Stop()
		u.timer = nil
	}
	u.scheduleFlushLocked()
}

func (u *unitFile) syncLocked() error {
	if err := u.flushLocked(); err != nil {
		return err
	}
	if u.f == nil {
		return nil
	}
	if u.store.onSync != nil {
		u.store.onSync()
	}
	return u.f.Sync()
}

func (u *unitFile) rotateLocked() error {
	if err := u.syncLocked(); err != nil {
		return err
	}
	if u.f != nil {
		_ = u.f.Close()
		u.f = nil
		u.w = nil
	}
	keep := u.store.keep
	if keep <= 0 {
		keep = DefaultKeep
	}
	base := u.path
	_ = os.Remove(fmt.Sprintf("%s.%d", base, keep))
	for i := keep - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", base, i)
		dst := fmt.Sprintf("%s.%d", base, i+1)
		_ = os.Rename(src, dst)
	}
	if err := os.Rename(base, base+".1"); err != nil && !os.IsNotExist(err) {
		_ = u.openLocked()
		return err
	}
	return u.openLocked()
}

func (u *unitFile) close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	if u.timer != nil {
		u.timer.Stop()
		u.timer = nil
	}
	err := u.syncLocked()
	if err != nil && u.w != nil {
		records, messageBytes := u.w.pendingLoss()
		u.store.queueMu.Lock()
		u.store.addStatsLocked(u.unit, CaptureStats{DroppedRecords: records, DroppedBytes: messageBytes})
		u.store.queueMu.Unlock()
	}
	if u.f != nil {
		if e := u.f.Close(); e != nil && err == nil {
			err = e
		}
		u.f = nil
		u.w = nil
	}
	return err
}

func (s *Store) syncUnit(unit string) error {
	f := s.fileExisting(unit)
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	return f.syncLocked()
}

func (s *Store) fileExisting(unit string) *unitFile {
	unit = canonicalUnit(unit)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		return nil
	}
	return s.files[unit]
}

// Read returns stored entries for unit, in write order. A missing file
// is an empty snapshot, not an error.
func (s *Store) Read(unit string) ([]Entry, error) {
	got, _, err := s.Query(unit, time.Time{}, "")
	return got, err
}

// Query returns entries for unit at or after since (zero = no lower bound),
// skipping through cursor (opaque, from a previous Query). The returned
// cursor is positioned after the last entry in the result, or equals the
// input cursor when the result is empty. Records whose unit field does
// not match are dropped (no cross-unit bleed).
//
// The current file is flushed under the per-unit write lock, then that
// lock is released before the scan. The current file is append-only, so
// reading it unlocked is safe; capture/append wait only for the flush.
func (s *Store) Query(unit string, since time.Time, cursor string) ([]Entry, string, error) {
	entries, next, _, err := s.QueryPage(unit, since, cursor, 0)
	return entries, next, err
}

// QueryPage bounds the sum of JSON-encoded Entry sizes (including separators).
// A zero budget is unlimited. The cursor always follows the last returned
// entry; more indicates that another entry was found beyond the page budget.
// Each call has a five-second deadline and shares bounded query admission.
func (s *Store) QueryPage(unit string, since time.Time, cursor string, maxBytes int) ([]Entry, string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultQueryTimeout)
	defer cancel()
	return s.QueryPageContext(ctx, unit, since, cursor, maxBytes)
}

func (s *Store) queryPage(ctx context.Context, unit string, since time.Time, cursor string, maxBytes int) ([]Entry, string, bool, error) {
	if s == nil {
		return nil, cursor, false, nil
	}
	unit = canonicalUnit(unit)

	if f := s.fileExisting(unit); f != nil {
		f.mu.Lock()
		if err := ctx.Err(); err != nil {
			f.mu.Unlock()
			return nil, cursor, false, err
		}
		if !f.closed {
			_ = f.flushLocked()
		}
		f.mu.Unlock()
	}
	return s.scan(ctx, unit, since, cursor, maxBytes)
}

func (s *Store) scan(ctx context.Context, unit string, since time.Time, cursor string, maxBytes int) ([]Entry, string, bool, error) {
	var out []Entry
	used := 0
	next := cursor
	past := cursor == ""
	wantID, wantN := parseCursor(cursor)
	cursorTS := cursorTime(wantID)
	seen := map[string]int{}
	base := s.path(unit)

	for _, path := range s.logPaths(unit) {
		if err := ctx.Err(); err != nil {
			return nil, cursor, false, err
		}
		archive := path != base
		if archive && !cursorTS.IsZero() {
			last := peekLastTimestamp(path)
			if !last.IsZero() && last.Before(cursorTS) {
				continue
			}
		}
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			if out == nil {
				out = []Entry{}
			}
			return out, next, false, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if err := ctx.Err(); err != nil {
				_ = f.Close()
				return nil, cursor, false, err
			}
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			e, ok := decodeRecord(line)
			if !ok {
				continue
			}
			if s.onScan != nil {
				s.onScan()
			}
			if canonicalUnit(e.Unit) != unit {
				continue
			}
			if !since.IsZero() && e.Timestamp.Before(since) {
				continue
			}
			if !past && !cursorTS.IsZero() && !e.Timestamp.IsZero() && e.Timestamp.Before(cursorTS) {
				continue
			}
			id := s.idFor(e)
			seen[id]++
			if !past {
				if id == wantID {
					if seen[id] <= wantN {
						if seen[id] == wantN {
							past = true
						}
						continue
					}
					past = true
				} else if archive && !e.Timestamp.IsZero() && e.Timestamp.After(cursorTS) {
					// Archives are complete generations. A timestamp after
					// the cursor means the cursor line is gone (rotated
					// off). The current file can have out-of-order stamps
					// from concurrent stdout/stderr capture, so it matches
					// the cursor by id only.
					past = true
				} else {
					continue
				}
			}
			if maxBytes > 0 {
				raw, err := json.Marshal(e)
				if err != nil {
					_ = f.Close()
					return out, next, false, err
				}
				if used+len(raw)+1 > maxBytes {
					_ = f.Close()
					if len(out) == 0 {
						return out, next, false, fmt.Errorf("journal entry exceeds log response budget")
					}
					return out, next, true, nil
				}
				used += len(raw) + 1
			}
			out = append(out, e)
			next = formatCursor(id, seen[id])
		}
		scanErr := sc.Err()
		_ = f.Close()
		if scanErr != nil {
			if out == nil {
				out = []Entry{}
			}
			return out, next, false, scanErr
		}
	}
	if out == nil {
		out = []Entry{}
	}
	return out, next, false, nil
}

func (s *Store) idFor(e Entry) string {
	if s != nil && s.onEntryID != nil {
		s.onEntryID()
	}
	return entryID(e)
}

func (s *Store) logPaths(unit string) []string {
	base := s.path(unit)
	keep := s.keep
	if keep <= 0 {
		keep = DefaultKeep
	}
	paths := make([]string, 0, keep+1)
	for i := keep; i >= 1; i-- {
		paths = append(paths, fmt.Sprintf("%s.%d", base, i))
	}
	paths = append(paths, base)
	return paths
}

func decodeRecord(line []byte) (Entry, bool) {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return Entry{}, false
	}
	e := Entry{
		Unit:         rec.Unit,
		PID:          rec.PID,
		Stream:       rec.Stream,
		Message:      rec.Message,
		InvocationID: rec.InvocationID,
		Severity:     rec.Severity,
		Session:      rec.Session,
		UserSID:      rec.UserSID,
		Continuation: rec.Continuation,
		Partial:      rec.Partial,
	}
	if rec.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
			e.Timestamp = t
		} else if t, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
			e.Timestamp = t
		}
	}
	return e, true
}

func entryID(e Entry) string {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d\x00%s\x00%s\x00%s", e.PID, e.Stream, e.Message, e.InvocationID)
	ts := ""
	if !e.Timestamp.IsZero() {
		ts = e.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	return ts + " " + strconv.FormatUint(h.Sum64(), 16)
}

func formatCursor(id string, n int) string {
	if n < 1 {
		n = 1
	}
	return id + " " + strconv.Itoa(n)
}

func parseCursor(cursor string) (id string, n int) {
	n = 1
	if cursor == "" {
		return "", 1
	}
	i := strings.LastIndexByte(cursor, ' ')
	if i < 0 {
		return cursor, 1
	}
	if v, err := strconv.Atoi(cursor[i+1:]); err == nil && v > 0 {
		return cursor[:i], v
	}
	return cursor, 1
}

func cursorTime(id string) time.Time {
	if id == "" {
		return time.Time{}
	}
	ts, _, _ := strings.Cut(id, " ")
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t
	}
	return time.Time{}
}

// peekLastTimestamp returns the timestamp of the last JSON line in path.
// A missing file, empty file, or undecodable tail yields the zero time
// (caller must not skip). Max line size matches the scanner (1 MiB).
func peekLastTimestamp(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return time.Time{}
	}
	const tail = 1024 * 1024
	size := st.Size()
	off := int64(0)
	n := size
	if size > tail {
		off = size - tail
		n = tail
	}
	buf := make([]byte, n)
	nr, _ := f.ReadAt(buf, off)
	if nr <= 0 {
		return time.Time{}
	}
	buf = buf[:nr]
	if off > 0 && bytes.IndexByte(buf, '\n') < 0 {
		return time.Time{}
	}
	line := lastNonEmptyLine(buf)
	if len(line) == 0 {
		return time.Time{}
	}
	e, ok := decodeRecord(line)
	if !ok {
		return time.Time{}
	}
	return e.Timestamp
}

func lastNonEmptyLine(buf []byte) []byte {
	for len(buf) > 0 && (buf[len(buf)-1] == '\n' || buf[len(buf)-1] == '\r') {
		buf = buf[:len(buf)-1]
	}
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		buf = buf[i+1:]
	}
	if len(buf) > 0 && buf[len(buf)-1] == '\r' {
		buf = buf[:len(buf)-1]
	}
	return buf
}

func (s *Store) path(unit string) string {
	return filepath.Join(s.dir, unitFileName(canonicalUnit(unit)))
}

func canonicalUnit(unit string) string {
	return strings.ToLower(strings.TrimSpace(unit))
}

// FollowWait is the server-side poll budget when LogsParams.Follow is set.
func FollowWait() time.Duration { return followWait }

// FollowPoll is the sleep between Follow re-reads.
func FollowPoll() time.Duration { return followPoll }
