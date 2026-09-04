package journal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

	mu     sync.Mutex
	closed bool
	files  map[string]*unitFile
	capWG  map[string]*sync.WaitGroup
	origin Origin

	// onOpen / onSync / onScan / onEntryID are test hooks (nil in production).
	// onOpen fires after a successful OpenFile; onSync fires immediately
	// before Sync; onScan fires once per decoded line during the unlocked
	// scan (write lock must not be held); onEntryID fires when a line's
	// cursor id is computed.
	onOpen    func()
	onSync    func()
	onScan    func()
	onEntryID func()
}

// Entry is one journal line (DESIGN.md §22). v=2 adds Severity, Session,
// and UserSID. v=1 lines decode with those fields empty.
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
}

type unitFile struct {
	mu     sync.Mutex
	unit   string
	path   string
	store  *Store
	f      *os.File
	w      *bufio.Writer
	size   int64
	timer  *time.Timer
	closed bool
}

// Open creates dir if needed and returns a store rooted there.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("journal directory required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{
		dir:        dir,
		maxSize:    DefaultMaxSize,
		keep:       DefaultKeep,
		flushEvery: DefaultFlushEvery,
		files:      make(map[string]*unitFile),
		capWG:      make(map[string]*sync.WaitGroup),
	}, nil
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
	if s == nil {
		return nil
	}
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
		if e := f.close(); e != nil && err == nil {
			err = e
		}
	}
	return err
}

// Attach writes stdout and stderr to the unit's journal file, tagging
// each line with invocationID (DESIGN.md §22, §24) and v=2 fields
// (severity from stream; session and user SID from SetOrigin). Empty
// invocationID is replaced with a new ID so isolated journal use still
// correlates a capture. Nil streams are ignored. A nil Store still
// drains so the child cannot block.
func (s *Store) Attach(unit string, pid int, invocationID string, stdout, stderr io.Reader) {
	if s == nil {
		go drain(stdout)
		go drain(stderr)
		return
	}
	unit = canonicalUnit(unit)
	if invocationID == "" {
		invocationID = NewInvocationID()
	}
	origin := s.snapshotOrigin()
	done := s.beginCapture(unit)
	go func() {
		defer done()
		s.capture(unit, pid, invocationID, origin, "stdout", stdout)
	}()
	go func() {
		defer done()
		s.capture(unit, pid, invocationID, origin, "stderr", stderr)
	}()
}

// Wait blocks until in-flight captures for unit have observed EOF,
// then Flush+Sync that unit's file (DESIGN.md §22: Sync on unit exit,
// not per line).
func (s *Store) Wait(unit string) {
	s.waitCaptures(context.Background(), unit, false)
}

// WaitContext is Wait with a deadline. If ctx fires first, the hung
// capture group is abandoned so a later Wait/Attach is not blocked
// (stopUnit TimeoutStopSec; issue #68). Returns false on timeout.
func (s *Store) WaitContext(ctx context.Context, unit string) bool {
	return s.waitCaptures(ctx, unit, true)
}

func (s *Store) waitCaptures(ctx context.Context, unit string, abandonOnCancel bool) bool {
	if s == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	unit = canonicalUnit(unit)
	s.mu.Lock()
	wg := s.capWG[unit]
	s.mu.Unlock()
	if wg == nil {
		s.syncUnit(unit)
		return true
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.syncUnit(unit)
		return true
	case <-ctx.Done():
		if abandonOnCancel {
			s.mu.Lock()
			if s.capWG[unit] == wg {
				delete(s.capWG, unit)
			}
			s.mu.Unlock()
		}
		s.syncUnit(unit)
		return false
	}
}

func (s *Store) beginCapture(unit string) func() {
	unit = canonicalUnit(unit)
	s.mu.Lock()
	prev := s.capWG[unit]
	wg := new(sync.WaitGroup)
	wg.Add(2)
	s.capWG[unit] = wg
	s.mu.Unlock()
	if prev != nil {
		prev.Wait()
	}
	return wg.Done
}

func (s *Store) capture(unit string, pid int, inv string, origin Origin, stream string, r io.Reader) {
	if r == nil {
		return
	}
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			msg := strings.TrimRight(line, "\r\n")
			if msg != "" {
				s.append(Entry{
					Timestamp:    time.Now().UTC(),
					Unit:         unit,
					PID:          pid,
					Stream:       stream,
					Message:      msg,
					InvocationID: inv,
					Severity:     SeverityFromStream(stream),
					Session:      origin.Session,
					UserSID:      origin.UserSID,
				})
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Store) append(e Entry) {
	if s == nil || e.Unit == "" {
		return
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
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	raw = append(raw, '\n')

	f := s.file(e.Unit)
	if f == nil {
		return
	}
	f.write(raw)
}

func (s *Store) file(unit string) *unitFile {
	unit = canonicalUnit(unit)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if s.files == nil {
		s.files = make(map[string]*unitFile)
	}
	f := s.files[unit]
	if f == nil {
		f = &unitFile{
			unit:  unit,
			path:  s.path(unit),
			store: s,
		}
		s.files[unit] = f
	}
	return f
}

func (u *unitFile) write(raw []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	if u.f == nil {
		if err := u.openLocked(); err != nil {
			return
		}
	}
	max := u.store.maxSize
	if max <= 0 {
		max = DefaultMaxSize
	}
	if u.size > 0 && u.size+int64(len(raw)) > max {
		if err := u.rotateLocked(); err != nil {
			return
		}
	}
	n, err := u.w.Write(raw)
	if err != nil {
		return
	}
	u.size += int64(n)
	u.scheduleFlushLocked()
}

func (u *unitFile) openLocked() error {
	f, err := os.OpenFile(u.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	size := int64(0)
	if err == nil {
		size = st.Size()
	}
	u.f = f
	u.w = bufio.NewWriter(f)
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
	u.timer = time.AfterFunc(every, func() {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.timer = nil
		if u.closed {
			return
		}
		_ = u.flushLocked()
	})
}

func (u *unitFile) flushLocked() error {
	if u.w == nil {
		return nil
	}
	return u.w.Flush()
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
	if u.f != nil {
		if e := u.f.Close(); e != nil && err == nil {
			err = e
		}
		u.f = nil
		u.w = nil
	}
	return err
}

func (s *Store) syncUnit(unit string) {
	f := s.fileExisting(unit)
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	_ = f.syncLocked()
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
func (s *Store) QueryPage(unit string, since time.Time, cursor string, maxBytes int) ([]Entry, string, bool, error) {
	if s == nil {
		return nil, cursor, false, nil
	}
	unit = canonicalUnit(unit)

	if f := s.fileExisting(unit); f != nil {
		f.mu.Lock()
		if !f.closed {
			_ = f.flushLocked()
		}
		f.mu.Unlock()
	}
	return s.scan(unit, since, cursor, maxBytes)
}

func (s *Store) scan(unit string, since time.Time, cursor string, maxBytes int) ([]Entry, string, bool, error) {
	var out []Entry
	used := 0
	next := cursor
	past := cursor == ""
	wantID, wantN := parseCursor(cursor)
	cursorTS := cursorTime(wantID)
	seen := map[string]int{}
	base := s.path(unit)

	for _, path := range s.logPaths(unit) {
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
