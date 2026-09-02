package journal

import (
	"fmt"
	"strconv"
	"strings"
)

// unitFileName is a reversible encoding of the canonical unit name so
// Windows-forbidden filename characters cannot collide (DESIGN.md §22).
// foo:bar and foo*bar must not share a file. Reserved device basenames
// (CON/PRN/AUX/NUL/COM1–9/LPT1–9) and trailing-dot/space names are
// percent-encoded so the on-disk name is not a device and stays reversible.
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
	} else {
		name = encodeReservedFileName(name)
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

func encodeReservedFileName(name string) string {
	for len(name) > 0 {
		last := name[len(name)-1]
		if last != '.' && last != ' ' {
			break
		}
		name = name[:len(name)-1] + fmt.Sprintf("%%%02X", last)
	}
	stem := name
	if i := strings.IndexByte(name, '.'); i >= 0 {
		stem = name[:i]
	}
	if reservedDeviceName(stem) && len(name) > 0 {
		name = fmt.Sprintf("%%%02X", name[0]) + name[1:]
	}
	return name
}

func reservedDeviceName(stem string) bool {
	s := strings.ToLower(stem)
	switch s {
	case "con", "prn", "aux", "nul":
		return true
	}
	if len(s) == 4 && (s[:3] == "com" || s[:3] == "lpt") && s[3] >= '1' && s[3] <= '9' {
		return true
	}
	return false
}

// decodeUnitFileName reverses unitFileName for tests (J1).
func decodeUnitFileName(filename string) (string, bool) {
	name, ok := strings.CutSuffix(filename, ".log")
	if !ok {
		return "", false
	}
	if name == "_unknown" {
		return "", true
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		if name[i] == '%' && i+2 < len(name) {
			v, err := strconv.ParseUint(name[i+1:i+3], 16, 8)
			if err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(name[i])
	}
	return b.String(), true
}
