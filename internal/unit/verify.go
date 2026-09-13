package unit

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MaxFileBytes bounds disk-backed unit parsing for both verification and reload.
const MaxFileBytes = 64 << 10

// VerifyPath reads a unit file from disk, parses it, and verifies it.
// It does not require a running daemon. A .registry, .eventlog, or .path
// unit also requires the companion basename .service next to the file.
func VerifyPath(path string) Report {
	f, err := os.Open(path)
	var data []byte
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if len(data) > MaxFileBytes {
			err = fmt.Errorf("unit file exceeds %d bytes", MaxFileBytes)
		}
	}
	if err != nil {
		return Report{Issues: []Issue{{
			Path:     path,
			Severity: SeverityError,
			Message:  err.Error(),
		}}}
	}
	name := UnitNameFromPath(path)
	rep := Parse(path, name, data)
	if rep.Unit != nil && companionKind(rep.Unit.Kind) {
		rep.Issues = append(rep.Issues, companionIssues(path, rep.Unit.Name)...)
	}
	return rep
}

func companionKind(k Kind) bool {
	return k == KindRegistry || k == KindEventLog || k == KindPath
}

// RegistryScopeIssues reports hive/scope errors. System manager: HKLM only.
// User manager: HKCU (that user) and HKLM.
func RegistryScopeIssues(u *Unit, userScope bool) []Issue {
	if u == nil || u.Registry == nil {
		return nil
	}
	var out []Issue
	for _, key := range u.Registry.Changed {
		if key.AllowedInScope(userScope) {
			continue
		}
		out = append(out, Issue{
			Path:     u.Path,
			Severity: SeverityError,
			Message:  "HKCU is not valid in the system manager (LocalSystem hive); use HKLM or a user manager",
		})
	}
	return out
}

// EventLogScopeIssues reports channel/scope errors. The system manager
// accepts any channel. A user manager rejects System and Security.
func EventLogScopeIssues(u *Unit, userScope bool) []Issue {
	if u == nil || u.EventLog == nil || !userScope {
		return nil
	}
	var out []Issue
	for _, tr := range u.EventLog.Triggers {
		if !tr.RestrictedInUserScope() {
			continue
		}
		out = append(out, Issue{
			Path:     u.Path,
			Severity: SeverityError,
			Message:  fmt.Sprintf("%s is not valid in a user manager; use Application or a custom log", tr.Channel),
		})
	}
	return out
}

func companionIssues(path, name string) []Issue {
	companion := CompanionService(name)
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), companion)); err != nil {
		return []Issue{{
			Path:     path,
			Severity: SeverityError,
			Message:  fmt.Sprintf("missing companion %s", companion),
		}}
	}
	return nil
}
