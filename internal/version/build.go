package version

import (
	"runtime"
	"runtime/debug"
)

// BuildInfo identifies the running binary. Commit and Modified come from the
// VCS data the Go toolchain embeds with -buildvcs; tools/build requires it.
// Test binaries and builds without VCS data have no Commit, and Modified is
// then nil (unknown).
type BuildInfo struct {
	Version  string
	Commit   string
	Modified *bool
	Go       string
}

// Build returns the identity of the running binary.
func Build() BuildInfo {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		bi = nil
	}
	return buildInfo(Version, bi)
}

func buildInfo(release string, bi *debug.BuildInfo) BuildInfo {
	out := BuildInfo{Version: release, Go: runtime.Version()}
	if bi == nil {
		return out
	}
	if bi.GoVersion != "" {
		out.Go = bi.GoVersion
	}
	var modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			out.Commit = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if out.Commit == "" {
		return out
	}
	switch modified {
	case "true":
		v := true
		out.Modified = &v
	case "false":
		v := false
		out.Modified = &v
	}
	return out
}
