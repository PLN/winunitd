package unit

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

func buildArgv(execRaw string, extraArgs []string) ([]string, error) {
	execRaw = strings.TrimSpace(execRaw)
	if execRaw == "" && len(extraArgs) == 0 {
		return nil, fmt.Errorf("ExecStart is empty")
	}

	var argv []string
	var err error
	switch {
	case isJSONArray(execRaw):
		argv, err = parseJSONArgv(execRaw)
		if err != nil {
			return nil, err
		}
	case len(extraArgs) > 0:
		// ExecStartArg is present: ExecStart is the unsplit executable path
		// (DESIGN.md §26). Stray compatibility-syntax arguments are rejected
		// by the parser before this runs.
		if execRaw == "" {
			return nil, fmt.Errorf("ExecStart is empty")
		}
		argv = []string{execRaw}
	default:
		argv, err = splitCommand(execRaw)
		if err != nil {
			return nil, err
		}
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf("ExecStart is empty")
	}
	return append(argv, extraArgs...), nil
}

func isJSONArray(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "[")
}

func parseJSONArgv(s string) ([]string, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON-array ExecStart: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("JSON-array ExecStart is empty")
	}
	out := make([]string, 0, len(raw))
	for i, item := range raw {
		var str string
		if err := json.Unmarshal(item, &str); err != nil {
			return nil, fmt.Errorf("JSON-array ExecStart[%d] must be a string", i)
		}
		out = append(out, str)
	}
	if out[0] == "" {
		return nil, fmt.Errorf("ExecStart is empty")
	}
	return out, nil
}

// splitCommand splits a compatibility ExecStart= line on unquoted whitespace.
// Double quotes group a token; backslash is literal (Windows path separator).
func splitCommand(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inQuote := false
	started := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			started = true
		case unicode.IsSpace(r) && !inQuote:
			if started {
				args = append(args, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated quote in ExecStart")
	}
	if started {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("ExecStart is empty")
	}
	return args, nil
}

// execStartHasUnquotedWhitespace reports whether s contains whitespace
// outside of double quotes.
func execStartHasUnquotedWhitespace(s string) bool {
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case unicode.IsSpace(r) && !inQuote:
			return true
		}
	}
	return false
}

// execStartHasStrayArgs reports the ExecStartArg footgun: ExecStart looks
// like a compatibility command line (executable plus extra argv) rather
// than a single path. DESIGN.md §26 still allows unquoted spaces in the
// path itself (C:\Program Files\Foo\foo.exe).
func execStartHasStrayArgs(execRaw string) bool {
	execRaw = strings.TrimSpace(execRaw)
	if execRaw == "" || isJSONArray(execRaw) {
		return false
	}
	if !execStartHasUnquotedWhitespace(execRaw) {
		return false
	}
	parts, err := splitCommand(execRaw)
	if err != nil || len(parts) < 2 {
		return false
	}
	if looksLikeWindowsExecutable(parts[0]) {
		return true
	}
	for _, p := range parts[1:] {
		if strings.HasPrefix(p, "-") {
			return true
		}
	}
	return false
}

func looksLikeWindowsExecutable(p string) bool {
	if !WindowsAbs(p) {
		return false
	}
	lower := strings.ToLower(p)
	for _, ext := range []string{".exe", ".com", ".bat", ".cmd", ".ps1"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}
