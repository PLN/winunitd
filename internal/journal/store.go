package journal

import (
	"bufio"
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

// FormatVersion is the on-disk journal record version (DESIGN.md §53).
const FormatVersion = 1

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
}

// Entry is one journal line (DESIGN.md §22).
type Entry struct {
	Timestamp    time.Time
	Unit         string
	PID          int
	Stream       string
	Message      string
	InvocationID string
}

type record struct {
	V            int    `json:"v"`
	Timestamp    string `json:"timestamp"`
	Unit         string `json:"unit"`
	PID          int    `json:"pid,omitempty"`
	Stream       string `json:"stream,omitempty"`
	Message      string `json:"message"`
	InvocationID string `json:"invocationId,omitempty"`
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
// each line with invocationID (DESIGN.md §22, §24). Empty invocationID
// is replaced with a new ID so isolated journal use still correlates
// a capture. Nil streams are ignored. A nil Store still drains so the
// child cannot block.
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
	done := s.beginCapture(unit)
	go func() {
		defer done()
		s.capture(unit, pid, invocationID, "stdout", stdout)
	}()
	go func() {
		defer done()
		s.capture(unit, pid, invocationID, "stderr", stderr)
	}()
}

// Wait blocks until in-flight captures for unit have observed EOF,
// then Flush+Sync that unit's file (DESIGN.md §22: Sync on unit exit,
// not per line).
func (s *Store) Wait(unit string) {
	if s == nil {
		return
	}
	unit = canonicalUnit(unit)
	s.mu.Lock()
	wg := s.capWG[unit]
	s.mu.Unlock()
	if wg != nil {
		wg.Wait()
	}
	s.syncUnit(unit)
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

func (s *Store) capture(unit string, pid int, inv, stream string, r io.Reader) {
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
	rec := record{
		V:            FormatVersion,
		Timestamp:    e.Timestamp.UTC().Format(time.RFC3339Nano),
		Unit:         e.Unit,
		PID:          e.PID,
		Stream:       e.Stream,
		Message:      e.Message,
		InvocationID: e.InvocationID,
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
func (s *Store) Query(unit string, since time.Time, cursor string) ([]Entry, string, error) {
	if s == nil {
		return nil, cursor, nil
	}
	unit = canonicalUnit(unit)

	if f := s.fileExisting(unit); f != nil {
		f.mu.Lock()
		if !f.closed {
			_ = f.flushLocked()
		}
		out, next, err := s.readLocked(unit, since, cursor)
		f.mu.Unlock()
		return out, next, err
	}
	return s.readLocked(unit, since, cursor)
}

func (s *Store) readLocked(unit string, since time.Time, cursor string) ([]Entry, string, error) {
	var out []Entry
	next := cursor
	past := cursor == ""
	wantID, wantN := parseCursor(cursor)
	cursorTS := cursorTime(wantID)
	seen := map[string]int{}

	for _, path := range s.logPaths(unit) {
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			if out == nil {
				out = []Entry{}
			}
			return out, next, err
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
			if canonicalUnit(e.Unit) != unit {
				continue
			}
			if !since.IsZero() && e.Timestamp.Before(since) {
				continue
			}
			id := entryID(e)
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
				} else if !e.Timestamp.IsZero() && e.Timestamp.After(cursorTS) {
					past = true
				} else {
					continue
				}
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
			return out, next, scanErr
		}
	}
	if out == nil {
		out = []Entry{}
	}
	return out, next, nil
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
