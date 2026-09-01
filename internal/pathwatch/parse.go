package pathwatch

import (
	"fmt"
	"strings"
)

// Spec is one PathChanged= or PathExists= value.
type Spec struct {
	Raw string
}

// Parse validates an absolute Windows path (drive-letter or UNC).
// POSIX-rooted paths such as /tmp/foo are rejected: the first-class
// target is Windows (same rule as ExecStart=).
func Parse(raw string) (Spec, error) {
	return parseAbs(raw, "PathChanged")
}

// ParseExists validates a PathExists= value (same absolute-path rule).
func ParseExists(raw string) (Spec, error) {
	return parseAbs(raw, "PathExists")
}

func parseAbs(raw, directive string) (Spec, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Spec{}, fmt.Errorf("empty path")
	}
	if !windowsAbs(s) {
		return Spec{}, fmt.Errorf("%s must be an absolute Windows path", directive)
	}
	return Spec{Raw: s}, nil
}

func windowsAbs(p string) bool {
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

// SplitDirName splits a Windows path into parent directory and final
// component. Drive roots keep a trailing slash (C:\).
func SplitDirName(p string) (dir, name string) {
	p = strings.TrimRight(strings.TrimSpace(p), `/\`)
	if p == "" {
		return "", ""
	}
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return p, ""
	}
	dir, name = p[:i], p[i+1:]
	if len(dir) == 2 && dir[1] == ':' {
		dir += `\`
	}
	if dir == "" {
		dir = `\`
	}
	return dir, name
}
