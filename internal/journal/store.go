package journal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// FormatVersion is the on-disk journal record version (DESIGN.md §53).
const FormatVersion = 1

// Store is a directory of per-unit append-only journal files.
type Store struct {
	dir    string
	mu     sync.Mutex
	unitMu map[string]*sync.Mutex
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

// Open creates dir if needed and returns a store rooted there.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("journal directory required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{
		dir:    dir,
		unitMu: make(map[string]*sync.Mutex),
		capWG:  make(map[string]*sync.WaitGroup),
	}, nil
}

// Dir is the journal root (<base-dir>\journal).
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
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

// Wait blocks until in-flight captures for unit have observed EOF.
// Call after closing that invocation's pipes so the next Attach cannot
// interleave old InvocationID= lines (C2 teardown vs relaunch).
func (s *Store) Wait(unit string) {
	if s == nil {
		return
	}
	mu := s.lockUnit(unit)
	mu.Lock()
	wg := s.capWG[unit]
	mu.Unlock()
	if wg != nil {
		wg.Wait()
	}
}

func (s *Store) beginCapture(unit string) func() {
	mu := s.lockUnit(unit)
	mu.Lock()
	prev := s.capWG[unit]
	wg := new(sync.WaitGroup)
	wg.Add(2)
	s.capWG[unit] = wg
	mu.Unlock()
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

	mu := s.lockUnit(e.Unit)
	mu.Lock()
	defer mu.Unlock()

	path := s.path(e.Unit)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(raw)
	_ = f.Sync()
	_ = f.Close()
}

// Read returns stored entries for unit, in write order. A missing file
// is an empty snapshot, not an error. Follow is not implemented here.
func (s *Store) Read(unit string) ([]Entry, error) {
	if s == nil {
		return nil, nil
	}
	mu := s.lockUnit(unit)
	mu.Lock()
	defer mu.Unlock()

	path := s.path(unit)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
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
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	if out == nil {
		out = []Entry{}
	}
	return out, nil
}

func (s *Store) lockUnit(unit string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unitMu == nil {
		s.unitMu = make(map[string]*sync.Mutex)
	}
	m, ok := s.unitMu[unit]
	if !ok {
		m = &sync.Mutex{}
		s.unitMu[unit] = m
	}
	return m
}

func (s *Store) path(unit string) string {
	return filepath.Join(s.dir, unitFileName(unit))
}

func unitFileName(unit string) string {
	if unit == "" {
		return "_unknown.log"
	}
	var b strings.Builder
	for _, r := range unit {
		if r < 32 || !unicode.IsPrint(r) || strings.ContainsRune(`\/:*?"<>|`, r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	name := b.String()
	if name == "" || name == "." || name == ".." {
		name = "_unknown"
	}
	return name + ".log"
}
