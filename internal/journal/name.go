package journal

import (
	"fmt"
	"strings"
)

// unitFileName is a reversible encoding of the canonical unit name so
// Windows-forbidden filename characters cannot collide (DESIGN.md §22).
// foo:bar and foo*bar must not share a file.
func unitFileName(unit string) string {
	unit = canonicalUnit(unit)
	if unit == "" {
		return "_unknown.log"
	}
	var b strings.Builder
	for i := 0; i < len(unit); i++ {
		c := unit[i]
		if safeFileByte(c) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	name := b.String()
	if name == "" || name == "." || name == ".." {
		name = "_unknown"
	}
	return name + ".log"
}

func safeFileByte(c byte) bool {
	if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
		return true
	}
	switch c {
	case '-', '_', '.', '@':
		return true
	}
	return false
}
