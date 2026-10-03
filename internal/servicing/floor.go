package servicing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
)

// FloorSchema is the only accepted floor record schema.
const FloorSchema = 1

// Bounds on a floor record.
const (
	MaxFloorBytes    = 16 << 10
	maxFloorFeatures = 64
	maxFeatureBytes  = 64
)

var featureName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// Floor is the minimum build that may admit hosted work. Every set field must
// be satisfied. It is a consumer's statement of what its admitted workloads
// need, not the newest installed build, so rollback to an earlier compatible
// package stays possible.
type Floor struct {
	Schema int `json:"schema"`
	// MinVersion is the lowest release, by semantic-version precedence.
	MinVersion string `json:"minVersion,omitempty"`
	// RequireFeatures are capability contract names the build must list.
	RequireFeatures []string `json:"requireFeatures,omitempty"`
	// RequireCleanBuild needs a known source revision and an explicit
	// unmodified tree. Unknown build state is not clean.
	RequireCleanBuild bool `json:"requireCleanBuild,omitempty"`
}

// Validate rejects a floor that is empty, malformed or of another schema.
func (f *Floor) Validate() error {
	if f == nil {
		return errors.New("floor is missing")
	}
	if f.Schema != FloorSchema {
		return fmt.Errorf("floor schema %d is not supported", f.Schema)
	}
	if f.MinVersion == "" && len(f.RequireFeatures) == 0 && !f.RequireCleanBuild {
		return errors.New("floor sets no requirement")
	}
	if f.MinVersion != "" {
		if _, err := ParseRelease(f.MinVersion); err != nil {
			return err
		}
	}
	if len(f.RequireFeatures) > maxFloorFeatures {
		return fmt.Errorf("floor requires more than %d features", maxFloorFeatures)
	}
	for i, name := range f.RequireFeatures {
		if len(name) > maxFeatureBytes || !featureName.MatchString(name) {
			return fmt.Errorf("floor feature %q is not a contract name", name)
		}
		if slices.Contains(f.RequireFeatures[:i], name) {
			return fmt.Errorf("floor repeats feature %q", name)
		}
	}
	return nil
}

// DecodeFloor strictly decodes and validates one floor record.
func DecodeFloor(data []byte) (*Floor, error) {
	if len(data) > MaxFloorBytes {
		return nil, fmt.Errorf("floor record exceeds %d bytes", MaxFloorBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f Floor
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("floor record: %w", err)
	}
	if dec.More() {
		return nil, errors.New("floor record has trailing data")
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// EncodeFloor validates f and returns its record with sorted features.
func EncodeFloor(f *Floor) ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	out := *f
	out.RequireFeatures = slices.Sorted(slices.Values(f.RequireFeatures))
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Build identifies a binary for floor evaluation.
type Build struct {
	Version string
	// Commit and Modified come from version-control data embedded at build
	// time. An empty Commit or nil Modified is unknown.
	Commit   string
	Modified *bool
	// Features lists the capability contracts the binary enforces. Nil means
	// the binary reports none, so it cannot satisfy a feature requirement.
	Features []string
}

// CurrentBuild describes the running binary from its release string and the
// version-control data the Go toolchain embedded.
func CurrentBuild(release string, features []string) Build {
	bi, _ := debug.ReadBuildInfo()
	return buildFromInfo(release, bi, features)
}

func buildFromInfo(release string, bi *debug.BuildInfo, features []string) Build {
	b := Build{Version: release, Features: slices.Clone(features)}
	if bi == nil {
		return b
	}
	var modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			b.Commit = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if b.Commit != "" && (modified == "true" || modified == "false") {
		m := modified == "true"
		b.Modified = &m
	}
	return b
}

// Verdict is the result of evaluating a build against a floor. Reasons are
// in a fixed order: version, features, then build state.
type Verdict struct {
	Satisfied bool
	Reasons   []string
}

// Evaluate checks b against f. A nil floor is satisfied.
func Evaluate(f *Floor, b Build) Verdict {
	if f == nil {
		return Verdict{Satisfied: true}
	}
	var reasons []string
	if f.MinVersion != "" {
		min, err := ParseRelease(f.MinVersion)
		got, gotErr := ParseRelease(b.Version)
		switch {
		case err != nil:
			reasons = append(reasons, "floor version is invalid")
		case gotErr != nil:
			reasons = append(reasons, fmt.Sprintf("build version %q is not a release version", b.Version))
		case got.Compare(min) < 0:
			reasons = append(reasons, fmt.Sprintf("version %s is below %s", b.Version, f.MinVersion))
		}
	}
	if len(f.RequireFeatures) > 0 {
		var missing []string
		for _, name := range f.RequireFeatures {
			if !slices.Contains(b.Features, name) {
				missing = append(missing, name)
			}
		}
		slices.Sort(missing)
		switch {
		case b.Features == nil:
			reasons = append(reasons, "build reports no features; requires "+strings.Join(missing, ", "))
		case len(missing) > 0:
			reasons = append(reasons, "missing features "+strings.Join(missing, ", "))
		}
	}
	if f.RequireCleanBuild {
		switch {
		case b.Commit == "":
			reasons = append(reasons, "build has no source revision")
		case b.Modified == nil:
			reasons = append(reasons, "build source state is unknown")
		case *b.Modified:
			reasons = append(reasons, "build is from a modified source tree")
		}
	}
	return Verdict{Satisfied: len(reasons) == 0, Reasons: reasons}
}

// AdmissionHold returns why a build must not admit hosted work under the
// floor record at path, or "" when admission may open. An absent record is no
// floor. A record that cannot be read, decoded or trusted holds admission.
func AdmissionHold(path string, b Build) string {
	f, err := ReadFloor(path)
	if err != nil {
		return "compatibility floor record is unusable: " + err.Error()
	}
	v := Evaluate(f, b)
	if v.Satisfied {
		return ""
	}
	return "below compatibility floor: " + strings.Join(v.Reasons, "; ")
}
