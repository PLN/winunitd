package protocol

import (
	"fmt"
	"strings"
	"unicode"
)

// DefaultPipeName is the system manager control pipe (DESIGN.md §30).
const DefaultPipeName = `\\.\pipe\winunitd\control`

// ControlPipeSDDL allows LocalSystem and Administrators only.
// D:P — protected DACL (no inherited ACEs). GA — generic all.
// SY — Local System (S-1-5-18). BA — Builtin Administrators (S-1-5-32-544).
const ControlPipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"

// UserPipeName is the per-user manager control pipe for sid
// (`\\.\pipe\winunitd\user\<SID>\control`). SID-keyed, not session-keyed.
func UserPipeName(sid string) string {
	return `\\.\pipe\winunitd\user\` + sid + `\control`
}

// UserPipeSDDL allows that user, LocalSystem, and Administrators.
func UserPipeSDDL(sid string) (string, error) {
	if !ValidSID(sid) {
		return "", fmt.Errorf("invalid SID %q", sid)
	}
	return "D:P(A;;GA;;;" + sid + ")(A;;GA;;;SY)(A;;GA;;;BA)", nil
}

// ValidSID reports whether s is an NT SID string (S-1-5-…), so it is safe
// to embed in an SDDL ACE.
func ValidSID(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "S-") {
		return false
	}
	parts := strings.Split(s, "-")
	if len(parts) < 3 {
		return false
	}
	for _, p := range parts[1:] {
		if p == "" {
			return false
		}
		for _, r := range p {
			if !unicode.IsDigit(r) {
				return false
			}
		}
	}
	return true
}
