package servicing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/PLN/winunitd/internal/version"
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

// DecodeFloor strictly decodes and validates one floor record: exactly one
// JSON object, then only whitespace; each known key at most once, spelled
// exactly; no unknown key; and no null. No field is nullable: omit an
// optional field instead. A present record that fails any of this is
// unusable and holds admission; it is never read as a weaker floor.
func DecodeFloor(data []byte) (*Floor, error) {
	if len(data) > MaxFloorBytes {
		return nil, fmt.Errorf("floor record exceeds %d bytes", MaxFloorBytes)
	}
	fields, err := decodeObject(data)
	if err != nil {
		return nil, fmt.Errorf("floor record: %w", err)
	}
	var f Floor
	for key, raw := range fields {
		var err error
		switch key {
		case "schema":
			err = json.Unmarshal(raw, &f.Schema)
		case "minVersion":
			err = json.Unmarshal(raw, &f.MinVersion)
		case "requireFeatures":
			err = json.Unmarshal(raw, &f.RequireFeatures)
		case "requireCleanBuild":
			err = json.Unmarshal(raw, &f.RequireCleanBuild)
		default:
			err = errors.New("unknown field")
		}
		if err != nil {
			return nil, fmt.Errorf("floor record field %q: %w", key, err)
		}
	}
	if _, ok := fields["schema"]; !ok {
		return nil, errors.New("floor record has no schema")
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// decodeObject reads one top-level JSON object into its raw values,
// rejecting a repeated key, a null value and anything after the object.
// encoding/json alone keeps the last of repeated keys, matches keys without
// regard to case and turns null into a zero value.
func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		if _, dup := fields[key]; dup {
			return nil, fmt.Errorf("repeated key %q", key)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, fmt.Errorf("key %q is null", key)
		}
		fields[key] = raw
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errors.New("unterminated object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after the object")
	}
	return fields, nil
}

// Within reports whether f requires nothing that prev did not, so every
// build prev admits also meets f. Replacing prev by such a floor only lowers
// it; anything else raises it in some respect. Nothing is within no floor.
func (f *Floor) Within(prev *Floor) bool {
	if prev == nil {
		return false
	}
	if f.MinVersion != "" {
		if prev.MinVersion == "" {
			return false
		}
		min, err := ParseRelease(f.MinVersion)
		was, prevErr := ParseRelease(prev.MinVersion)
		if err != nil || prevErr != nil || min.Compare(was) > 0 {
			return false
		}
	}
	for _, name := range f.RequireFeatures {
		if !slices.Contains(prev.RequireFeatures, name) {
			return false
		}
	}
	return !f.RequireCleanBuild || prev.RequireCleanBuild
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

// runningFeatures is the capability feature list of this build. It stays
// nil until the capability query provides the system endpoint's features;
// until then a floor that requires features holds admission and refuses
// packages, and floor set refuses it.
var runningFeatures []string

// Running identifies this binary for the compatibility floor. The system
// manager, the offline floor verbs and the package helper all use it, so
// they evaluate one identity: the release linked into the binary, the
// version-control state the toolchain embedded and the capability features.
func Running() Build {
	return CurrentBuild(version.Version, runningFeatures)
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
