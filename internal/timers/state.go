package timers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store persists last scheduled/actual/successful execution under
// <dir>/<name>.json (DESIGN.md §17, §32).
type Store struct {
	dir  string
	load func(string) (Runtime, error) // fault injection, configured before use
	save func(string, Runtime) error
}

type persisted struct {
	Activation    *Activation `json:"activation,omitempty"`
	Version       int         `json:"version,omitempty"`
	LastScheduled string      `json:"lastScheduled,omitempty"`
	LastActual    string      `json:"lastActual,omitempty"`
	LastSuccess   string      `json:"lastSuccess,omitempty"`
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
	rt, _ := s.LoadChecked(name)
	return rt
}

// LoadChecked distinguishes missing state from corrupt or unreadable state.
// Legacy unversioned timestamps are accepted and upgraded by the next save.
func (s *Store) LoadChecked(name string) (Runtime, error) {
	if s != nil && s.load != nil {
		return s.load(name)
	}
	path := s.path(name)
	if path == "" {
		return Runtime{}, nil
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return Runtime{}, nil
	}
	if err != nil {
		return Runtime{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil {
		return Runtime{}, err
	}
	if len(data) > 16384 {
		return Runtime{}, errors.New("timer state exceeds 16 KiB")
	}
	var p persisted
	if strings.TrimSpace(string(data)) == "null" {
		return Runtime{}, errors.New("timer state must be an object")
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return Runtime{}, err
	}
	if p.Version != 0 && p.Version != 1 && p.Version != 2 {
		return Runtime{}, fmt.Errorf("unsupported timer state version %d", p.Version)
	}
	var rt Runtime
	if p.Activation != nil {
		if p.Version != 2 {
			return Runtime{}, errors.New("activation intent requires timer state version 2")
		}
		if err := validateActivation(*p.Activation); err != nil {
			return Runtime{}, err
		}
		rt.Activation = *p.Activation
	}
	for _, field := range []struct {
		raw string
		dst *time.Time
	}{{p.LastScheduled, &rt.LastScheduled}, {p.LastActual, &rt.LastActual}, {p.LastSuccess, &rt.LastSuccess}} {
		raw, dst := field.raw, field.dst
		if raw == "" {
			continue
		}
		stamp, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return Runtime{}, errors.New("invalid timer state timestamp")
		}
		*dst = stamp
	}
	return rt, nil
}

// Save writes last-run times. In-memory FiredBoot/FiredStartup are not stored:
// OnStartupSec is per winunitd instance; OnBootSec is recomputed from boot.
func (s *Store) Save(name string, rt Runtime) error {
	if s != nil && s.save != nil {
		return s.save(name, rt)
	}
	path := s.path(name)
	if path == "" {
		return nil
	}
	p := persisted{
		Version:       2,
		LastScheduled: formatStamp(rt.LastScheduled),
		LastActual:    formatStamp(rt.LastActual),
		LastSuccess:   formatStamp(rt.LastSuccess),
	}
	if rt.Activation != (Activation{}) {
		if err := validateActivation(rt.Activation); err != nil {
			return err
		}
		activation := rt.Activation
		p.Activation = &activation
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.CreateTemp(s.dir, ".timer-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func formatStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func validateActivation(a Activation) error {
	if a.ID == "" || len(a.ID) > 128 || len(a.Unit) > 255 || strings.ContainsAny(a.Unit, "\\/:\x00\r\n") || a.Scheduled.IsZero() || a.Actual.IsZero() {
		return errors.New("invalid timer activation intent")
	}
	if a.Result != "pending" && a.Result != "success" && a.Result != "failed" {
		return errors.New("invalid timer activation result")
	}
	return nil
}
