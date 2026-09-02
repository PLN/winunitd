package pathwatch

import "strings"

// joinDirName joins a Windows directory and a single path component.
func joinDirName(dir, name string) string {
	dir = strings.TrimRight(strings.TrimSpace(dir), `/\`)
	name = strings.Trim(name, `/\`)
	if dir == "" || name == "" {
		return ""
	}
	if len(dir) == 2 && dir[1] == ':' {
		return dir + `\` + name
	}
	return dir + `\` + name
}

// winPathParts splits a Windows path into case-insensitive components.
// UNC paths keep a leading empty part so \\server\share does not collide
// with a drive-letter path of the same remainder.
func winPathParts(p string) []string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `/`, `\`)
	unc := strings.HasPrefix(p, `\\`)
	p = strings.Trim(p, `\`)
	if p == "" {
		return nil
	}
	parts := strings.Split(p, `\`)
	if unc {
		return append([]string{""}, parts...)
	}
	return parts
}

func winPathEqual(a, b string) bool {
	ap, bp := winPathParts(a), winPathParts(b)
	if len(ap) != len(bp) {
		return false
	}
	for i := range ap {
		if !strings.EqualFold(ap[i], bp[i]) {
			return false
		}
	}
	return true
}

// isTargetLeafWatch reports whether watchDir+filter is the target's
// parent directory and basename (the watch that sees create/delete/rename
// of the PathExists= path itself).
func isTargetLeafWatch(target, watchDir, filter string) bool {
	parent, name := SplitDirName(target)
	return winPathEqual(parent, watchDir) && strings.EqualFold(name, filter)
}

// nextExistsStep returns the next closer ancestor watch when filter is an
// intermediate component of target under watchDir. ok is false when the
// current watch is already the target leaf or is not on the path to target.
func nextExistsStep(target, watchDir, filter string) (nextDir, nextFilter string, ok bool) {
	targetParts := winPathParts(target)
	dirParts := winPathParts(watchDir)
	if len(dirParts) == 0 || len(targetParts) <= len(dirParts) {
		return "", "", false
	}
	for i := range dirParts {
		if !strings.EqualFold(targetParts[i], dirParts[i]) {
			return "", "", false
		}
	}
	first := targetParts[len(dirParts)]
	if !strings.EqualFold(first, filter) {
		return "", "", false
	}
	if len(targetParts) == len(dirParts)+1 {
		return "", "", false
	}
	nextFilter = targetParts[len(dirParts)+1]
	if nextFilter == "" {
		return "", "", false
	}
	return joinDirName(watchDir, first), nextFilter, true
}
