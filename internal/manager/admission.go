package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

// UserAdmission is copied on installation; callers cannot mutate live policy.
type UserAdmission struct {
	Mode  string            `json:"mode"`
	Users map[string]string `json:"users"`
}

// SetUserAdmission publishes an immutable policy before attempting revocation.
// Failed stops remain owned and can be retried by applying the same policy.
func (h *UserHost) SetUserAdmission(p UserAdmission) error {
	p, err := p.validated()
	if err != nil {
		return err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fmt.Errorf("user host is closed")
	}
	if !reflect.DeepEqual(p, h.admission) {
		h.admission = p
		h.admissionRevision++
	}
	var revoked []string
	for sid := range h.bySID {
		allow, probe := p.decision(sid)
		if !allow && !probe && !h.lingeringLocked(sid) {
			revoked = append(revoked, sid)
		}
	}
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultStopTimeout)
	defer cancel()
	var result error
	for _, sid := range revoked {
		unlock, err := h.ops.lockContext(ctx, sid)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		h.mu.Lock()
		allow, probe := h.admission.decision(sid)
		inst := h.bySID[sid]
		revoke := !allow && !probe && !h.lingeringLocked(sid)
		if revoke {
			for session, mapped := range h.sessions {
				if mapped == sid {
					delete(h.sessions, session)
				}
			}
		}
		h.mu.Unlock()
		if revoke {
			result = errors.Join(result, h.killUserInstance(ctx, sid, inst))
		}
		unlock()
	}
	return result
}

// WatchUserAdmission reconciles file-based opt-in and administrator edits.
// One loop owns reads/reconciliation, so slow probes cannot spawn more loops.
func (h *UserHost) WatchUserAdmission(ctx context.Context, path string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		p, err := LoadUserAdmission(path)
		if err != nil {
			h.cfg.Logf("user admission policy: %v", err)
			h.mu.Lock()
			p = h.admission
			h.mu.Unlock()
		}
		if err := h.SetUserAdmission(p); err != nil {
			h.cfg.Logf("user admission reconciliation: %v", err)
		}
		if ctx.Err() != nil {
			return
		}
		h.Reconcile()
	}
}

func (p UserAdmission) validated() (UserAdmission, error) {
	if p.Mode == "" {
		p.Mode = "explicit"
	}
	if p.Mode != "explicit" && p.Mode != "unit-files" {
		return UserAdmission{}, fmt.Errorf("unknown user admission mode")
	}
	out := UserAdmission{Mode: p.Mode, Users: make(map[string]string, len(p.Users))}
	for sid, rule := range p.Users {
		if !protocol.ValidSID(sid) {
			return UserAdmission{}, fmt.Errorf("invalid user admission SID")
		}
		if rule != "enabled" && rule != "disabled" && rule != "inherit" {
			return UserAdmission{}, fmt.Errorf("invalid user admission override")
		}
		out.Users[sid] = rule
	}
	return out, nil
}

// decision returns whether admission is granted or requires a user-file probe.
func (p UserAdmission) decision(sid string) (allow, probe bool) {
	if !protocol.ValidSID(sid) {
		return false, false
	}
	switch p.Users[sid] {
	case "enabled":
		return true, false
	case "disabled":
		return false, false
	}
	return false, p.Mode == "unit-files"
}

// LoadUserAdmission reads a bounded, administrator-owned machine policy.
// Missing policy means explicit admission with an empty allowlist.
func LoadUserAdmission(path string) (UserAdmission, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return (UserAdmission{}).validated()
	}
	if err != nil {
		return UserAdmission{}, err
	}
	defer f.Close()
	if err := admissionFileTrusted(f); err != nil {
		return UserAdmission{}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return UserAdmission{}, err
	}
	if len(data) > 64<<10 {
		return UserAdmission{}, fmt.Errorf("user admission policy exceeds 64 KiB")
	}
	return parseUserAdmission(data)
}

func parseUserAdmission(data []byte) (UserAdmission, error) {
	if err := uniquePolicyKeys(json.NewDecoder(bytes.NewReader(data)), 0); err != nil {
		return UserAdmission{}, err
	}
	var p UserAdmission
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return UserAdmission{}, fmt.Errorf("invalid user admission policy: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return UserAdmission{}, fmt.Errorf("user admission policy has trailing content")
	}
	return p.validated()
}

func uniquePolicyKeys(d *json.Decoder, depth int) error {
	if depth > 4 {
		return fmt.Errorf("user admission policy nesting exceeds limit")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return fmt.Errorf("invalid user admission JSON")
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate or invalid user admission key")
			}
			if depth == 0 && name != "mode" && name != "users" {
				return fmt.Errorf("unknown user admission field")
			}
			seen[name] = true
		}
		if err := uniquePolicyKeys(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}
