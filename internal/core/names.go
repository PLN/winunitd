package core

import (
	"github.com/PLN/winunitd/internal/unit"
)

// NormalizeName applies DESIGN.md §36: unit names are case-insensitive
// and stored in lower-case; an unqualified name defaults to .service.
func NormalizeName(name string) string {
	return unit.NormalizeName(name)
}

func uniqueStable(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = NormalizeName(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func containsName(ss []string, name string) bool {
	for _, s := range ss {
		if s == name {
			return true
		}
	}
	return false
}

func cloneNames(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
