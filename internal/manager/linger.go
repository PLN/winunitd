package manager

import (
	"encoding/json"
	"fmt"
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
	return os.WriteFile(s.path(rec.SID), append(data, '\n'), 0o600)
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
	data, err := os.ReadFile(s.path(sid))
	if err != nil {
		return rec, err
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
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []runtime.LingerRecord
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		sid := e.Name()
		if !protocol.ValidSID(sid) {
			continue
		}
		rec, err := s.Get(sid)
		if err != nil {
			continue
		}
		out = append(out, rec)
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
