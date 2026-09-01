package registry

import (
	"fmt"
	"strings"
)

// Hive is a supported Windows registry root.
type Hive int

const (
	// HKLM is HKEY_LOCAL_MACHINE.
	HKLM Hive = iota
	// HKCU is HKEY_CURRENT_USER (that user in a user manager).
	HKCU
)

func (h Hive) String() string {
	switch h {
	case HKLM:
		return "HKLM"
	case HKCU:
		return "HKCU"
	default:
		return "hive"
	}
}

// Key is one RegistryChanged= value.
type Key struct {
	Hive Hive
	Path string // Software\Example (no hive prefix)
	Raw  string
}

// ParseKey parses HKLM\… or HKCU\… . PowerShell drives (HKLM:\) are rejected.
func ParseKey(raw string) (Key, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Key{}, fmt.Errorf("empty registry path")
	}
	if strings.Contains(s, ":") {
		return Key{}, fmt.Errorf("PowerShell drive syntax is not allowed (use HKLM\\... or HKCU\\...)")
	}

	var hive Hive
	switch {
	case hasHivePrefix(s, "HKLM"):
		hive = HKLM
		s = s[4:]
	case hasHivePrefix(s, "HKCU"):
		hive = HKCU
		s = s[4:]
	default:
		return Key{}, fmt.Errorf("hive must be HKLM\\ or HKCU\\")
	}
	if !strings.HasPrefix(s, `\`) {
		return Key{}, fmt.Errorf("hive must be HKLM\\ or HKCU\\")
	}
	path := strings.Trim(s, `\`)
	if path == "" {
		return Key{}, fmt.Errorf("empty registry path")
	}
	return Key{Hive: hive, Path: path, Raw: strings.TrimSpace(raw)}, nil
}

// AllowedInScope reports whether this key may be watched in this manager.
// The system manager accepts HKLM only (HKCU there is LocalSystem's hive).
// A user manager accepts HKCU (that user) and HKLM.
func (k Key) AllowedInScope(userScope bool) bool {
	switch k.Hive {
	case HKLM:
		return true
	case HKCU:
		return userScope
	default:
		return false
	}
}

func hasHivePrefix(s, hive string) bool {
	if len(s) < len(hive) {
		return false
	}
	return strings.EqualFold(s[:len(hive)], hive)
}
