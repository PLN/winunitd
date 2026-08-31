package timers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store persists last scheduled/actual/successful execution under
// <dir>/<name>.json (DESIGN.md §17, §32).
type Store struct {
	dir string
}

type persisted struct {
	LastScheduled string `json:"lastScheduled,omitempty"`
	LastActual    string `json:"lastActual,omitempty"`
	LastSuccess   string `json:"lastSuccess,omitempty"`
}

// OpenStore creates dir if needed. An empty dir disables persistence.
func OpenStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return &Store{}, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(name string) string {
	if s == nil || s.dir == "" {
		return ""
	}
	base := filepath.Base(name)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return filepath.Join(s.dir, base+".json")
}

// Load returns stored last-run times. Missing files yield a zero Runtime.
func (s *Store) Load(name string) Runtime {
	path := s.path(name)
	if path == "" {
		return Runtime{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Runtime{}
	}
	var p persisted
	if json.Unmarshal(data, &p) != nil {
		return Runtime{}
	}
	return Runtime{
		LastScheduled: parseStamp(p.LastScheduled),
		LastActual:    parseStamp(p.LastActual),
		LastSuccess:   parseStamp(p.LastSuccess),
	}
}

// Save writes last-run times. In-memory FiredBoot/FiredStartup are not stored:
// OnStartupSec is per winunitd instance; OnBootSec is recomputed from boot.
func (s *Store) Save(name string, rt Runtime) error {
	path := s.path(name)
	if path == "" {
		return nil
	}
	p := persisted{
		LastScheduled: formatStamp(rt.LastScheduled),
		LastActual:    formatStamp(rt.LastActual),
		LastSuccess:   formatStamp(rt.LastSuccess),
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func parseStamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func formatStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
