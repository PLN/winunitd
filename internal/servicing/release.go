// Package servicing holds the compatibility floor: the minimum build that may
// admit hosted work on a machine. The floor is persisted under the protected
// machine data root and evaluated before any admission, by the manager at
// start-up and by the product MSI before it replaces files (#262).
package servicing

import (
	"fmt"
	"strconv"
	"strings"
)

// Release is a parsed release string, MAJOR.MINOR.PATCH with an optional
// prerelease, ordered by semantic-versioning 2.0 precedence. Build metadata
// after '+' is accepted and does not affect ordering.
type Release struct {
	Major, Minor, Patch uint64
	Pre                 []string
}

// ParseRelease parses a release string such as 0.1.0-alpha or 0.2.1-beta.
func ParseRelease(s string) (Release, error) {
	core, _, _ := strings.Cut(s, "+")
	core, pre, hasPre := strings.Cut(core, "-")
	fields := strings.Split(core, ".")
	if len(fields) != 3 {
		return Release{}, fmt.Errorf("release %q is not MAJOR.MINOR.PATCH", s)
	}
	var nums [3]uint64
	for i, f := range fields {
		n, ok := numericIdentifier(f)
		if !ok {
			return Release{}, fmt.Errorf("release %q has an invalid numeric field", s)
		}
		nums[i] = n
	}
	r := Release{Major: nums[0], Minor: nums[1], Patch: nums[2]}
	if hasPre {
		for _, id := range strings.Split(pre, ".") {
			if !validPrereleaseIdentifier(id) {
				return Release{}, fmt.Errorf("release %q has an invalid prerelease", s)
			}
			r.Pre = append(r.Pre, id)
		}
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		for _, id := range strings.Split(s[i+1:], ".") {
			if id == "" || strings.Trim(id, alnumHyphen) != "" {
				return Release{}, fmt.Errorf("release %q has invalid build metadata", s)
			}
		}
	}
	return r, nil
}

const alnumHyphen = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-"

// numericIdentifier accepts a decimal without leading zeros.
func numericIdentifier(s string) (uint64, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

func validPrereleaseIdentifier(s string) bool {
	if s == "" || strings.Trim(s, alnumHyphen) != "" {
		return false
	}
	if strings.Trim(s, "0123456789") == "" {
		_, ok := numericIdentifier(s)
		return ok
	}
	return true
}

// Compare returns -1, 0 or 1 as r orders before, equal to or after o. A
// prerelease orders before its release; prerelease identifiers compare
// numerically when both are numeric, numeric before alphanumeric, and a
// shorter list before a longer one with an equal prefix.
func (r Release) Compare(o Release) int {
	for _, d := range [][2]uint64{{r.Major, o.Major}, {r.Minor, o.Minor}, {r.Patch, o.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(r.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(r.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(r.Pre) && i < len(o.Pre); i++ {
		if c := comparePrerelease(r.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(r.Pre) < len(o.Pre):
		return -1
	case len(r.Pre) > len(o.Pre):
		return 1
	}
	return 0
}

func comparePrerelease(a, b string) int {
	an, aNum := numericIdentifier(a)
	bn, bNum := numericIdentifier(b)
	switch {
	case aNum && bNum:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		}
		return 0
	case aNum:
		return -1
	case bNum:
		return 1
	}
	return strings.Compare(a, b)
}
