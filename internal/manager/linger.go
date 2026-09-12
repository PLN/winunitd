package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

// LingerStore is the tiny linger records under
// C:\ProgramData\winunitd\linger\<SID> (DESIGN.md §15, §32). Not NTFS
// symlinks. CredentialURI is an optional named CredMan/LSA reference.
type LingerStore struct {
	dir string
}

const maxLingerRecordBytes = 64 * 1024
const maxLingerDirectoryEntries = 4096

func (s *LingerStore) entries() ([]os.DirEntry, error) {
	f, err := os.Open(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(maxLingerDirectoryEntries + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > maxLingerDirectoryEntries {
		return nil, errors.New("linger directory entry limit exceeded")
	}
	count := 0
	for _, e := range entries {
		if protocol.ValidSID(e.Name()) {
			count++
		}
	}
	if count > maxTrackedUserManagers {
		return nil, errors.New("linger record count limit exceeded")
	}
	return entries, nil
}

// OpenLingerStore uses dir as the linger root. The directory is created
// on the first Put.
func OpenLingerStore(dir string) *LingerStore {
	return &LingerStore{dir: dir}
}

func (s *LingerStore) path(sid string) string {
	return filepath.Join(s.dir, sid)
}

// Put writes rec. Passwords and unknown secret fields are rejected.
func (s *LingerStore) Put(rec runtime.LingerRecord) error {
	if s == nil || s.dir == "" {
		return fmt.Errorf("linger store is not configured")
	}
	if !protocol.ValidSID(rec.SID) {
		return fmt.Errorf("invalid linger SID %q", rec.SID)
	}
	if rec.CredentialURI != "" {
		canon, err := runtime.ParseCredentialURI(rec.CredentialURI)
		if err != nil {
			return err
		}
		rec.CredentialURI = canon
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	entries, err := s.entries()
	if err != nil {
		return err
	}
	count, exists := 0, false
	for _, e := range entries {
		if protocol.ValidSID(e.Name()) {
			count++
		}
		if e.Name() == rec.SID {
			exists = true
		}
	}
	if !exists && count >= maxTrackedUserManagers {
		return errors.New("linger record count limit exceeded")
	}
	body := lingerFile{
		SID:           rec.SID,
		Name:          rec.Name,
		CredentialURI: rec.CredentialURI,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if err := rejectSecretJSON(data); err != nil {
		return err
	}
	if len(data)+1 > maxLingerRecordBytes {
		return errors.New("linger record byte limit exceeded")
	}
	// A failed write must not truncate a previously accepted grant. Publish only
	// a complete closed file; callers serialize this with snapshot observation.
	f, err := os.CreateTemp(s.dir, ".linger-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, writeErr := f.Write(append(data, '\n'))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return os.Rename(name, s.path(rec.SID))
}

// Get reads the linger record for sid.
func (s *LingerStore) Get(sid string) (runtime.LingerRecord, error) {
	var rec runtime.LingerRecord
	if s == nil || s.dir == "" {
		return rec, fmt.Errorf("linger store is not configured")
	}
	if !protocol.ValidSID(sid) {
		return rec, fmt.Errorf("invalid linger SID %q", sid)
	}
	f, err := os.Open(s.path(sid))
	if err != nil {
		return rec, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxLingerRecordBytes+1))
	if err != nil {
		return rec, err
	}
	if len(data) > maxLingerRecordBytes {
		return rec, errors.New("linger record byte limit exceeded")
	}
	if err := rejectSecretJSON(data); err != nil {
		return rec, err
	}
	var body lingerFile
	if err := json.Unmarshal(data, &body); err != nil {
		return rec, fmt.Errorf("linger record %s: %w", sid, err)
	}
	if body.SID == "" {
		body.SID = sid
	}
	if body.SID != sid {
		return rec, fmt.Errorf("linger record SID %s does not match filename %s", body.SID, sid)
	}
	if body.CredentialURI != "" {
		canon, err := runtime.ParseCredentialURI(body.CredentialURI)
		if err != nil {
			return rec, err
		}
		body.CredentialURI = canon
	}
	return runtime.LingerRecord{SID: body.SID, Name: body.Name, CredentialURI: body.CredentialURI}, nil
}

// Has reports whether a linger record exists for sid.
func (s *LingerStore) Has(sid string) bool {
	if s == nil || !protocol.ValidSID(sid) {
		return false
	}
	_, err := os.Stat(s.path(sid))
	return err == nil
}

// Delete removes the linger record. Missing is not an error.
func (s *LingerStore) Delete(sid string) error {
	if s == nil || s.dir == "" {
		return fmt.Errorf("linger store is not configured")
	}
	if !protocol.ValidSID(sid) {
		return fmt.Errorf("invalid linger SID %q", sid)
	}
	err := os.Remove(s.path(sid))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List returns every linger record in the store.
func (s *LingerStore) List() ([]runtime.LingerRecord, error) {
	if s == nil || s.dir == "" {
		return nil, nil
	}
	entries, err := s.entries()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []runtime.LingerRecord
	var invalid int
	for _, e := range entries {
		sid := e.Name()
		if !protocol.ValidSID(sid) {
			continue
		}
		rec, err := s.Get(sid)
		if err != nil {
			invalid++
			continue
		}
		out = append(out, rec)
	}
	if invalid != 0 {
		return out, fmt.Errorf("%d invalid or unreadable linger records", invalid)
	}
	return out, nil
}

type lingerFile struct {
	SID           string `json:"sid"`
	Name          string `json:"name,omitempty"`
	CredentialURI string `json:"credentialURI,omitempty"`
}

func rejectSecretJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k := range raw {
		kl := strings.ToLower(strings.TrimSpace(k))
		switch kl {
		case "password", "passwd", "secret", "credential", "credentialblob", "passwordfile":
			return fmt.Errorf("linger record must not contain %q", k)
		}
	}
	return nil
}
