package unit

import (
	"fmt"
	"strings"
)

type iniFile struct {
	sections []iniSection
}

type iniSection struct {
	name    string
	line    int
	entries []iniEntry
}

type iniEntry struct {
	key   string
	value string
	line  int
}

func parseINI(src []byte, path string) (iniFile, []Issue) {
	src = stripBOM(src)
	lines := splitLines(string(src))
	joined, issues := joinContinuations(lines, path)

	var file iniFile
	var cur *iniSection
	for _, rl := range joined {
		trim := strings.TrimSpace(rl.text)
		if trim == "" || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, ";") {
			continue
		}
		if strings.HasPrefix(trim, "[") {
			if !strings.HasSuffix(trim, "]") {
				issues = append(issues, Issue{
					Path:     path,
					Line:     rl.line,
					Severity: SeverityError,
					Message:  fmt.Sprintf("malformed section header %q", trim),
				})
				continue
			}
			name := strings.TrimSpace(trim[1 : len(trim)-1])
			if name == "" {
				issues = append(issues, Issue{
					Path:     path,
					Line:     rl.line,
					Severity: SeverityError,
					Message:  "empty section name",
				})
				continue
			}
			file.sections = append(file.sections, iniSection{name: name, line: rl.line})
			cur = &file.sections[len(file.sections)-1]
			continue
		}
		if cur == nil {
			issues = append(issues, Issue{
				Path:     path,
				Line:     rl.line,
				Severity: SeverityError,
				Message:  "directive outside of a section",
			})
			continue
		}
		key, value, ok := strings.Cut(rl.text, "=")
		if !ok {
			issues = append(issues, Issue{
				Path:     path,
				Line:     rl.line,
				Severity: SeverityError,
				Message:  fmt.Sprintf("missing '=' in %q", trim),
			})
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			issues = append(issues, Issue{
				Path:     path,
				Line:     rl.line,
				Severity: SeverityError,
				Message:  "empty directive name",
			})
			continue
		}
		cur.entries = append(cur.entries, iniEntry{key: key, value: value, line: rl.line})
	}
	return file, issues
}

type rawLine struct {
	line int
	text string
}

func joinContinuations(lines []string, path string) ([]rawLine, []Issue) {
	var out []rawLine
	var issues []Issue
	i := 0
	for i < len(lines) {
		text := strings.TrimRight(lines[i], "\r")
		n := i + 1
		for strings.HasSuffix(text, `\`) {
			if i+1 >= len(lines) {
				issues = append(issues, Issue{
					Path:     path,
					Line:     n,
					Severity: SeverityError,
					Message:  "line continuation at end of file",
				})
				text = strings.TrimSuffix(text, `\`)
				break
			}
			i++
			text = strings.TrimSuffix(text, `\`) + lines[i]
		}
		out = append(out, rawLine{line: n, text: text})
		i++
	}
	return out, issues
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

func stripBOM(src []byte) []byte {
	if len(src) >= 3 && src[0] == 0xEF && src[1] == 0xBB && src[2] == 0xBF {
		return src[3:]
	}
	return src
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "yes", "y", "true", "t", "on":
		return true, nil
	case "0", "no", "n", "false", "f", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", s)
	}
}

func parseUnitNames(s string) []string {
	return strings.Fields(s)
}
