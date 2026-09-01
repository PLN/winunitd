package unit

import (
	"fmt"
	"strings"
)

// NormalizeName applies DESIGN.md §36: unit names are case-insensitive
// and stored in lower-case; an unqualified name defaults to .service.
func NormalizeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	if _, err := KindFromName(name); err == nil {
		return name
	}
	return name + ".service"
}

// KindFromName returns the unit kind implied by a file name.
func KindFromName(name string) (Kind, error) {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".service"):
		return KindService, nil
	case strings.HasSuffix(n, ".timer"):
		return KindTimer, nil
	case strings.HasSuffix(n, ".target"):
		return KindTarget, nil
	case strings.HasSuffix(n, ".registry"):
		return KindRegistry, nil
	case strings.HasSuffix(n, ".eventlog"):
		return KindEventLog, nil
	case strings.HasSuffix(n, ".path"):
		return KindPath, nil
	default:
		if i := strings.LastIndex(name, "."); i >= 0 {
			return "", fmt.Errorf("unsupported unit type %q", name[i:])
		}
		return "", fmt.Errorf("unit name %q has no type suffix", name)
	}
}

// UnitNameFromPath returns the unit file name from a filesystem path,
// accepting both Windows and POSIX separators so tests can run on any GOOS.
func UnitNameFromPath(path string) string {
	p := strings.TrimRight(path, `/\`)
	if p == "" {
		return path
	}
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// WindowsAbs reports whether p is an absolute Windows path.
// Drive-letter paths (C:\...) and UNC paths (\\server\share\...) count.
// POSIX-style rooted paths such as /usr/bin/foo do not: SearchPath=no
// and the first-class target is Windows.
func WindowsAbs(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, `//`) {
		return len(p) > 2
	}
	if len(p) >= 3 && isDriveLetter(p[0]) && p[1] == ':' && isSlash(p[2]) {
		return true
	}
	return false
}

func isDriveLetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func isSlash(b byte) bool {
	return b == '\\' || b == '/'
}
