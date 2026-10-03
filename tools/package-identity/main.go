// Command package-identity checks, before an MSI is built, that the package
// helper and the payload binaries are one build: the same source revision,
// unmodified tree, release value, toolchain and target, read from the
// produced binaries themselves, and that the payload files match their build
// manifest. The helper evaluates the compatibility floor with its own
// identity on the package's behalf, so a package whose helper differs from
// its payload is not admissible. It prints the shared identity as JSON.
//
// With -development, a modified tree is accepted and the identity is marked
// not admissible: such a package is for development only and must not enter
// a qualified servicing lane. Disagreement between binaries always fails.
package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
)

// versionFlag is the linker flag that sets the release value.
const versionFlag = "-X github.com/PLN/winunitd/internal/version.Version="

type artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// buildManifest is the part of tools/build's manifest this check reads.
type buildManifest struct {
	Schema    int        `json:"schema"`
	Version   string     `json:"version"`
	Commit    string     `json:"commit"`
	Dirty     bool       `json:"dirty"`
	Go        string     `json:"go"`
	GOOS      string     `json:"goos"`
	GOARCH    string     `json:"goarch"`
	Artifacts []artifact `json:"artifacts"`
}

// Identity is the build every checked binary shares.
type Identity struct {
	Release    string `json:"release"`
	Commit     string `json:"commit"`
	Modified   bool   `json:"modified"`
	Go         string `json:"go"`
	Target     string `json:"target"`
	Helper     string `json:"helperSha256"`
	Admissible bool   `json:"admissible"`
}

var fullCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

func main() {
	manifestPath := flag.String("manifest", "", "payload build-manifest.json")
	release := flag.String("release", "", "release value both builds were given")
	helper := flag.String("helper", "", "package helper executable")
	development := flag.Bool("development", false, "accept a modified tree and mark the package not admissible")
	flag.Parse()
	if *manifestPath == "" || *release == "" || *helper == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: package-identity -manifest FILE -release VERSION -helper FILE [-development]")
		os.Exit(2)
	}
	id, err := run(*manifestPath, *release, *helper, *development)
	if err != nil {
		fmt.Fprintln(os.Stderr, "package-identity:", err)
		os.Exit(1)
	}
	out, _ := json.Marshal(id)
	fmt.Println(string(out))
}

func run(manifestPath, release, helper string, development bool) (Identity, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return Identity{}, err
	}
	var m buildManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Identity{}, fmt.Errorf("build manifest: %w", err)
	}
	binaries := map[string]*debug.BuildInfo{}
	dir := filepath.Dir(manifestPath)
	for _, a := range m.Artifacts {
		path := filepath.Join(dir, a.Name)
		sum, err := fileSHA256(path)
		if err != nil {
			return Identity{}, err
		}
		if sum != a.SHA256 {
			return Identity{}, fmt.Errorf("%s does not match its build manifest", a.Name)
		}
		if !strings.EqualFold(filepath.Ext(a.Name), ".exe") {
			continue
		}
		bi, err := buildinfo.ReadFile(path)
		if err != nil {
			return Identity{}, fmt.Errorf("%s: %w", a.Name, err)
		}
		binaries[a.Name] = bi
	}
	bi, err := buildinfo.ReadFile(helper)
	if err != nil {
		return Identity{}, fmt.Errorf("helper: %w", err)
	}
	binaries["helper "+filepath.Base(helper)] = bi
	helperSum, err := fileSHA256(helper)
	if err != nil {
		return Identity{}, err
	}
	return check(m, release, binaries, helperSum, development)
}

// check requires every binary to carry the manifest's build identity.
func check(m buildManifest, release string, binaries map[string]*debug.BuildInfo, helperSum string, development bool) (Identity, error) {
	if m.Schema != 2 || m.Version != release || !fullCommit.MatchString(m.Commit) {
		return Identity{}, errors.New("build manifest does not name this release and a full source revision")
	}
	if m.Dirty && !development {
		return Identity{}, errors.New("payload was built from a modified tree; a modified build is development only (-development)")
	}
	if len(binaries) < 2 {
		return Identity{}, errors.New("no payload executable to compare with the helper")
	}
	want := map[string]string{
		"vcs.revision": m.Commit,
		"vcs.modified": fmt.Sprint(m.Dirty),
		"-trimpath":    "true",
		"CGO_ENABLED":  "0",
		"GOOS":         m.GOOS,
		"GOARCH":       m.GOARCH,
	}
	for name, bi := range binaries {
		if bi.GoVersion != m.Go {
			return Identity{}, fmt.Errorf("%s was built with %s, not %s", name, bi.GoVersion, m.Go)
		}
		got := map[string]string{}
		for _, s := range bi.Settings {
			got[s.Key] = s.Value
		}
		for key, value := range want {
			if got[key] != value {
				return Identity{}, fmt.Errorf("%s has %s %q, want %q", name, key, got[key], value)
			}
		}
		if !hasVersionFlag(got["-ldflags"], release) {
			return Identity{}, fmt.Errorf("%s does not link release %s", name, release)
		}
	}
	return Identity{Release: release, Commit: m.Commit, Modified: m.Dirty, Go: m.Go, Target: m.GOOS + "/" + m.GOARCH,
		Helper: helperSum, Admissible: !m.Dirty}, nil
}

// hasVersionFlag reports whether ldflags sets the release value exactly
// once, to release.
func hasVersionFlag(ldflags, release string) bool {
	n := strings.Count(ldflags, versionFlag)
	if n != 1 {
		return false
	}
	_, rest, _ := strings.Cut(ldflags, versionFlag)
	value, _, _ := strings.Cut(rest, " ")
	return value == release
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
