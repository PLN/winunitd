package core

import (
	"strings"

	"github.com/PLN/winunitd/internal/unit"
)

// NormalizeName applies DESIGN.md §36: an unqualified name defaults to
// .service. Names that already have a known suffix are left unchanged.
func NormalizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if _, err := unit.KindFromName(name); err == nil {
		return name
	}
	return name + ".service"
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
