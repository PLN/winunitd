package unit

import (
	"encoding/json"
	"fmt"
	"os"
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
		// (DESIGN.md §26). The parser rejects unquoted leftover argv when
		// that raw string does not Stat as a single file.
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

// execStartArgFootgun reports the ExecStartArg mix-up: ExecStartArg is in
// use, raw ExecStart has unquoted whitespace, and the raw string does not
// Stat as a single file (DESIGN.md §26 still allows a real path with spaces).
func execStartArgFootgun(execRaw string, stat func(string) (os.FileInfo, error)) bool {
	execRaw = strings.TrimSpace(execRaw)
	if execRaw == "" || isJSONArray(execRaw) {
		return false
	}
	if !execStartHasUnquotedWhitespace(execRaw) {
		return false
	}
	if stat == nil {
		stat = os.Stat
	}
	fi, err := stat(execRaw)
	if err == nil && fi != nil && !fi.IsDir() {
		return false
	}
	return true
}
