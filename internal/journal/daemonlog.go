package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// DaemonDirName is the machine-data child that holds the daemon log.
	DaemonDirName = "daemon"
	// DaemonFileName is the current daemon log file inside DaemonDirName.
	DaemonFileName = "daemon.log"

	DaemonEventLifecycleRejected = "lifecycle.rejected"
	DaemonEventStartLimit        = "lifecycle.start-limit"
	DaemonEventOpen              = "daemon.open"
	DaemonEventClose             = "daemon.close"

	maxDaemonQueueRecords = 32
	maxDaemonQueueBytes   = 16 << 10
	maxDaemonFieldBytes   = 160
	maxDaemonRecordBytes  = 1024
	maxDaemonFileBytes    = 256 << 10
	maxDaemonRecent       = 16
	maxDaemonTailBytes    = 16 << 10
	daemonLogVersion      = 1
)

// DaemonEvent is a structured daemon diagnostic. Only the fields below are
// persisted. Unknown map keys passed through RecordFields are ignored.
// Environment assignments, credential or store URIs, and parser dumps are
// omitted rather than truncated.
type DaemonEvent struct {
	Code                string
	Unit                string
	InvocationID        string
	OperationID         string
	ConfigRevision      string
	LoadState           string
	ActiveState         string
	Health              string
	Reason              string
	RestartAttempt      uint32
	StartLimitBurst     int
	StartLimitRemaining int
	HasStartLimit       bool
	HasRemaining        bool
}

// DaemonEventView is one accepted record. Acceptance is not a disk flush;
// write failures increment DaemonLogStats.Errors.
type DaemonEventView struct {
	Timestamp           string
	Code                string
	Unit                string
	InvocationID        string
	OperationID         string
	ConfigRevision      string
	LoadState           string
	ActiveState         string
	Health              string
	Reason              string
	RestartAttempt      uint32
	StartLimitBurst     *int
	StartLimitRemaining *int
}

// DaemonLogStats is the in-memory loss and recent-event view. Reading it does
// not touch the log file.
type DaemonLogStats struct {
	DroppedRecords uint64
	DroppedBytes   uint64
	Errors         uint64
	LastError      string
	Events         []DaemonEventView
}

type daemonRecord struct {
	V                   int    `json:"v"`
	Timestamp           string `json:"timestamp"`
	Code                string `json:"code"`
	Unit                string `json:"unit,omitempty"`
	InvocationID        string `json:"invocationId,omitempty"`
	OperationID         string `json:"operationId,omitempty"`
	ConfigRevision      string `json:"configRevision,omitempty"`
	LoadState           string `json:"loadState,omitempty"`
	ActiveState         string `json:"activeState,omitempty"`
	Health              string `json:"health,omitempty"`
	Reason              string `json:"reason,omitempty"`
	RestartAttempt      uint32 `json:"restartAttempt,omitempty"`
	StartLimitBurst     *int   `json:"startLimitBurst,omitempty"`
	StartLimitRemaining *int   `json:"startLimitRemaining,omitempty"`
}

// DaemonLog is a single append-only file under <root>/daemon/daemon.log.
// Record never waits on the writer. CloseContext stops waiting when ctx ends;
// a stalled sink keeps the file until the write returns, then exits.
type DaemonLog struct {
	root    string
	path    string
	archive string

	mu             sync.Mutex
	queue          [][]byte
	queued         int
	droppedRecords uint64
	droppedBytes   uint64
	errors         uint64
	lastError      string
	recent         []DaemonEventView
	shutdown       bool
	exitErr        error
	size           int64

	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	file      *os.File
	write     func([]byte) error
}

// DaemonLogPath is <root>/daemon/daemon.log.
func DaemonLogPath(root string) string {
	return filepath.Join(root, DaemonDirName, DaemonFileName)
}

// OpenDaemonLog creates a protected log inside root. write, when non-nil, runs
// in the writer before the file write so tests can stall or fail the sink.
// A nil log accepts nothing. Open reads at most 16 KiB of an existing tail so
// a restart can show recent records; that read is not on the status path.
func OpenDaemonLog(root string, write func([]byte) error) (*DaemonLog, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("daemon log root required")
	}
	root, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, DaemonDirName)
	path := filepath.Join(dir, DaemonFileName)
	if !withinRoot(root, dir) || !withinRoot(root, path) {
		return nil, fmt.Errorf("daemon log escapes data root")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if err := rejectSymlink(root); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := rejectSymlinkChain(root, path); err != nil {
		return nil, err
	}
	if err := protectDaemonPath(dir, true); err != nil {
		return nil, err
	}
	f, err := openContained(root, path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	l := &DaemonLog{
		root: root, path: path, archive: path + ".1",
		recent: readDaemonTail(f),
		size:   st.Size(),
		wake:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		file:   f,
		write:  write,
	}
	go l.loop()
	return l, nil
}

// UseDaemonLog attaches the durable log that RecordDiagnostic also feeds.
func (s *Store) UseDaemonLog(l *DaemonLog) {
	if s == nil {
		return
	}
	s.daemonLog.Store(l)
}

// RecordFields copies allowlisted keys and drops every other key before encode.
func (l *DaemonLog) RecordFields(fields map[string]string) {
	if l == nil {
		return
	}
	var ev DaemonEvent
	for key, value := range fields {
		switch key {
		case "code":
			ev.Code = value
		case "unit":
			ev.Unit = value
		case "invocationId":
			ev.InvocationID = value
		case "operationId":
			ev.OperationID = value
		case "configRevision":
			ev.ConfigRevision = value
		case "loadState":
			ev.LoadState = value
		case "activeState":
			ev.ActiveState = value
		case "health":
			ev.Health = value
		case "reason":
			ev.Reason = value
		}
	}
	l.Record(ev)
}

// Record queues one event without waiting for the sink. Unknown codes and
// oversized or forbidden payloads increment the drop counters and return.
func (l *DaemonLog) Record(ev DaemonEvent) {
	if l == nil {
		return
	}
	line, view, ok := encodeDaemonEvent(ev)
	if !ok {
		l.noteDrop(len(ev.Code) + len(ev.Reason))
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.shutdown || len(l.queue) >= maxDaemonQueueRecords || l.queued+len(line) > maxDaemonQueueBytes {
		l.droppedRecords++
		l.droppedBytes += uint64(len(line))
		return
	}
	l.queue = append(l.queue, line)
	l.queued += len(line)
	l.recent = append(l.recent, view)
	if len(l.recent) > maxDaemonRecent {
		l.recent = append([]DaemonEventView(nil), l.recent[len(l.recent)-maxDaemonRecent:]...)
	}
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Stats copies counters and the recent accepted tail.
func (l *DaemonLog) Stats() DaemonLogStats {
	if l == nil {
		return DaemonLogStats{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	events := make([]DaemonEventView, len(l.recent))
	for i, ev := range l.recent {
		events[i] = cloneDaemonView(ev)
	}
	return DaemonLogStats{
		DroppedRecords: l.droppedRecords,
		DroppedBytes:   l.droppedBytes,
		Errors:         l.errors,
		LastError:      l.lastError,
		Events:         events,
	}
}

func cloneDaemonView(ev DaemonEventView) DaemonEventView {
	if ev.StartLimitBurst != nil {
		n := *ev.StartLimitBurst
		ev.StartLimitBurst = &n
	}
	if ev.StartLimitRemaining != nil {
		n := *ev.StartLimitRemaining
		ev.StartLimitRemaining = &n
	}
	return ev
}

// CloseContext requests shutdown and waits until ctx ends. A stalled write
// does not extend the wait; the writer still closes the file after the sink returns.
func (l *DaemonLog) CloseContext(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.beginClose()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-l.done:
		l.mu.Lock()
		err := l.exitErr
		l.mu.Unlock()
		return err
	case <-ctx.Done():
		return fmt.Errorf("daemon log close: %w", ctx.Err())
	}
}

func (l *DaemonLog) beginClose() {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.shutdown = true
		close(l.stop)
		l.mu.Unlock()
	})
}

func (l *DaemonLog) noteDrop(n int) {
	if n < 0 {
		n = 0
	}
	l.mu.Lock()
	l.droppedRecords++
	l.droppedBytes += uint64(n)
	l.mu.Unlock()
}

func (l *DaemonLog) noteError(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	if textForbidden(msg) || looksLikeParserDump(msg) || len(msg) > maxDaemonFieldBytes || !utf8.ValidString(msg) {
		msg = "daemon log write failed"
	}
	l.mu.Lock()
	l.errors++
	l.lastError = msg
	l.mu.Unlock()
}

func (l *DaemonLog) loop() {
	defer close(l.done)
	var err error
	for {
		item, ok := l.pop()
		if !ok {
			break
		}
		if werr := l.writeOne(item); werr != nil {
			l.noteError(werr)
			err = errors.Join(err, werr)
		}
	}
	if l.file != nil {
		err = errors.Join(err, l.file.Sync(), l.file.Close())
		l.file = nil
	}
	l.mu.Lock()
	l.exitErr = err
	l.mu.Unlock()
}

func (l *DaemonLog) pop() ([]byte, bool) {
	for {
		l.mu.Lock()
		if len(l.queue) > 0 {
			item := l.queue[0]
			l.queue[0] = nil
			l.queue = l.queue[1:]
			l.queued -= len(item)
			l.mu.Unlock()
			return item, true
		}
		shutdown := l.shutdown
		l.mu.Unlock()
		if shutdown {
			return nil, false
		}
		select {
		case <-l.wake:
		case <-l.stop:
		}
	}
}

func (l *DaemonLog) writeOne(line []byte) error {
	if l.write != nil {
		if err := l.write(line); err != nil {
			return err
		}
	}
	if l.file == nil {
		f, err := openContained(l.root, l.path)
		if err != nil {
			return err
		}
		l.file = f
		l.size = 0
	}
	if l.size+int64(len(line)+1) > maxDaemonFileBytes {
		if err := l.rotate(); err != nil {
			return err
		}
	}
	n, err := l.file.Write(append(append([]byte(nil), line...), '\n'))
	l.size += int64(n)
	if err != nil {
		return err
	}
	l.mu.Lock()
	idle := len(l.queue) == 0
	l.mu.Unlock()
	if !idle {
		return nil
	}
	return l.file.Sync()
}

func (l *DaemonLog) rotate() error {
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			l.file = nil
			return err
		}
		l.file = nil
	}
	if !withinRoot(l.root, l.archive) {
		return fmt.Errorf("daemon log archive escapes data root")
	}
	if err := rejectSymlinkChain(l.root, l.archive); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(l.archive); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(l.path, l.archive); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := openContained(l.root, l.path)
	if err != nil {
		return err
	}
	l.file = f
	l.size = 0
	return nil
}

func openContained(root, path string) (*os.File, error) {
	if !withinRoot(root, path) {
		return nil, fmt.Errorf("daemon log escapes data root")
	}
	if err := rejectSymlinkChain(root, path); err != nil {
		return nil, err
	}
	f, err := openAppend(path)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	final, err := finalPath(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !pathWithin(resolved, final) {
		_ = f.Close()
		return nil, fmt.Errorf("daemon log escapes data root")
	}
	if err := protectDaemonPath(path, false); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func withinRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func rejectSymlinkChain(root, path string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !withinRoot(root, path) && root != path {
		return fmt.Errorf("daemon log escapes data root")
	}
	if err := rejectSymlink(root); err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		if err := rejectSymlink(cur); err != nil {
			return err
		}
	}
	return nil
}

func rejectSymlink(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("daemon log path is a symlink")
	}
	return nil
}

func encodeDaemonEvent(ev DaemonEvent) ([]byte, DaemonEventView, bool) {
	if !knownDaemonCode(ev.Code) {
		return nil, DaemonEventView{}, false
	}
	rec := daemonRecord{
		V:              daemonLogVersion,
		Timestamp:      time.Now().UTC().Format(time.RFC3339Nano),
		Code:           ev.Code,
		Unit:           cleanDaemonField(ev.Unit),
		InvocationID:   cleanDaemonField(ev.InvocationID),
		OperationID:    cleanDaemonField(ev.OperationID),
		ConfigRevision: cleanDaemonField(ev.ConfigRevision),
		LoadState:      cleanDaemonField(ev.LoadState),
		ActiveState:    cleanDaemonField(ev.ActiveState),
		Health:         cleanDaemonField(ev.Health),
		Reason:         cleanDaemonReason(ev.Reason),
		RestartAttempt: ev.RestartAttempt,
	}
	if ev.HasStartLimit {
		burst := ev.StartLimitBurst
		rec.StartLimitBurst = &burst
	}
	if ev.HasRemaining {
		remaining := ev.StartLimitRemaining
		rec.StartLimitRemaining = &remaining
	}
	line, err := json.Marshal(rec)
	if err != nil || payloadForbidden(line) {
		rec.Unit, rec.InvocationID, rec.OperationID = "", "", ""
		rec.ConfigRevision, rec.LoadState, rec.ActiveState, rec.Health, rec.Reason = "", "", "", "", ""
		line, err = json.Marshal(rec)
	}
	if err != nil || len(line) > maxDaemonRecordBytes || payloadForbidden(line) {
		return nil, DaemonEventView{}, false
	}
	return line, viewFromRecord(rec), true
}

func knownDaemonCode(code string) bool {
	switch code {
	case DaemonEventLifecycleRejected, DaemonEventStartLimit, DaemonEventOpen, DaemonEventClose:
		return true
	default:
		return false
	}
}

func cleanDaemonField(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxDaemonFieldBytes || textForbidden(s) || !utf8.ValidString(s) {
		return ""
	}
	return s
}

func cleanDaemonReason(s string) string {
	s = cleanDaemonField(s)
	if looksLikeParserDump(s) {
		return ""
	}
	return s
}

func textForbidden(s string) bool {
	if strings.ContainsAny(s, "\r\n\x00") {
		return true
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "environment=") {
		return true
	}
	for _, needle := range secretFragments() {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func diagnosticForbidden(s string) bool {
	return textForbidden(s) || looksLikeParserDump(s)
}

func secretFragments() []string {
	return []string{
		"pass" + "word=",
		"credential" + "uri",
		"store-uri",
		"credman:",
		"wincred:",
		"://",
	}
}

func looksLikeParserDump(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == ':' && s[i+1] >= '0' && s[i+1] <= '9' {
			return true
		}
	}
	return false
}

func payloadForbidden(b []byte) bool {
	if len(b) == 0 || bytes.Contains(b, []byte{'\n'}) || bytes.Contains(b, []byte{'\r'}) {
		return true
	}
	return textForbidden(string(b))
}

func viewFromRecord(rec daemonRecord) DaemonEventView {
	return DaemonEventView{
		Timestamp: rec.Timestamp, Code: rec.Code, Unit: rec.Unit,
		InvocationID: rec.InvocationID, OperationID: rec.OperationID,
		ConfigRevision: rec.ConfigRevision, LoadState: rec.LoadState,
		ActiveState: rec.ActiveState, Health: rec.Health, Reason: rec.Reason,
		RestartAttempt: rec.RestartAttempt, StartLimitBurst: rec.StartLimitBurst,
		StartLimitRemaining: rec.StartLimitRemaining,
	}
}

func readDaemonTail(f *os.File) []DaemonEventView {
	if f == nil {
		return nil
	}
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return nil
	}
	size := st.Size()
	window := size
	if window > maxDaemonTailBytes {
		window = maxDaemonTailBytes
	}
	buf := make([]byte, window)
	if _, err := f.ReadAt(buf, size-window); err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	if size > window {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			return nil
		}
	}
	var out []DaemonEventView
	for len(buf) > 0 {
		line, rest, _ := bytes.Cut(buf, []byte("\n"))
		buf = rest
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		view, ok := decodeDaemonEvent(line)
		if ok {
			out = append(out, view)
		}
	}
	if len(out) > maxDaemonRecent {
		out = append([]DaemonEventView(nil), out[len(out)-maxDaemonRecent:]...)
	}
	return out
}

func decodeDaemonEvent(line []byte) (DaemonEventView, bool) {
	if payloadForbidden(line) {
		return DaemonEventView{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var rec daemonRecord
	if err := dec.Decode(&rec); err != nil {
		return DaemonEventView{}, false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return DaemonEventView{}, false
	}
	if rec.V != daemonLogVersion || !knownDaemonCode(rec.Code) {
		return DaemonEventView{}, false
	}
	if cleanDaemonField(rec.Unit) != rec.Unit || cleanDaemonField(rec.InvocationID) != rec.InvocationID ||
		cleanDaemonField(rec.OperationID) != rec.OperationID || cleanDaemonField(rec.ConfigRevision) != rec.ConfigRevision ||
		cleanDaemonField(rec.LoadState) != rec.LoadState || cleanDaemonField(rec.ActiveState) != rec.ActiveState ||
		cleanDaemonField(rec.Health) != rec.Health || cleanDaemonReason(rec.Reason) != rec.Reason {
		return DaemonEventView{}, false
	}
	return viewFromRecord(rec), true
}
