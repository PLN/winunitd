package unit

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// formatVersions are the accepted [Unit] FormatVersion values, oldest first.
// The parser and the capability report both read this table.
var formatVersions = []int{1, 2}

// FormatVersions returns the accepted FormatVersion values, oldest first.
func FormatVersions() []int {
	return slices.Clone(formatVersions)
}

// formatVersion accepts only the exact decimal spelling of a supported value.
func formatVersion(s string) (int, bool) {
	for _, v := range formatVersions {
		if s == strconv.Itoa(v) {
			return v, true
		}
	}
	return 0, false
}

func formatVersionList() string {
	parts := make([]string, len(formatVersions))
	for i, v := range formatVersions {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ", ")
}

// Directives returns the recognized directive names by section, each sorted.
// Recognition is not acceptance: format, kind, and type rules still apply,
// and verify reports them for a concrete file. Names outside this set are
// always errors.
func Directives() map[string][]string {
	out := make(map[string][]string, len(knownDirectives))
	for section, names := range knownDirectives {
		out[section] = slices.Sorted(maps.Keys(names))
	}
	return out
}
