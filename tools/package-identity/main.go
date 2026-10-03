// Command package-identity checks, before an MSI is built, that the package
// helper and the payload binaries are one build: the same source revision,
// unmodified tree, toolchain and target, read from the produced binaries'
// embedded build information, and the same release value, read from the
// version variable linked into each binary that has one. -trimpath keeps
// the linker flags out of the build information, so the value itself is
// read. The payload files must match their build manifest. The helper
// evaluates the compatibility floor with its own identity on the package's
// behalf, so a package whose helper differs from its payload is not
// admissible. It prints the shared identity as JSON.
//
// With -development, a modified tree is accepted and the identity is marked
// not admissible: such a package is for development only and must not enter
// a qualified servicing lane. Disagreement between binaries always fails.
package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
)

// versionSymbol is the release variable the build sets with -X.
const versionSymbol = "github.com/PLN/winunitd/internal/version.Version"

// executable is one produced binary: its embedded build information and
// the release value linked into it, when it links the version variable.
type executable struct {
	info    *debug.BuildInfo
	release string
	linked  bool
	// needsRelease marks the daemon and the helper, which evaluate the
	// floor with their linked release.
	needsRelease bool
}

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
	binaries := map[string]executable{}
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
		b, err := readBinary(path)
		if err != nil {
			return Identity{}, fmt.Errorf("%s: %w", a.Name, err)
		}
		b.needsRelease = strings.EqualFold(a.Name, "winunitd.exe")
		binaries[a.Name] = b
	}
	b, err := readBinary(helper)
	if err != nil {
		return Identity{}, fmt.Errorf("helper: %w", err)
	}
	b.needsRelease = true
	binaries["helper "+filepath.Base(helper)] = b
	helperSum, err := fileSHA256(helper)
	if err != nil {
		return Identity{}, err
	}
	return check(m, release, binaries, helperSum, development)
}

func readBinary(path string) (executable, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return executable{}, err
	}
	release, linked, err := linkedRelease(path)
	if err != nil {
		return executable{}, err
	}
	return executable{info: info, release: release, linked: linked}, nil
}

// linkedRelease reads the version variable's value from a PE executable's
// symbol table and data: the string header the symbol names, then the bytes
// it points to.
func linkedRelease(path string) (string, bool, error) {
	f, err := pe.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	var base uint64
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		base = oh.ImageBase
	case *pe.OptionalHeader32:
		base = uint64(oh.ImageBase)
	default:
		return "", false, errors.New("no optional header")
	}
	for _, sym := range f.Symbols {
		if sym.Name != versionSymbol {
			continue
		}
		if sym.SectionNumber < 1 || int(sym.SectionNumber) > len(f.Sections) {
			return "", false, errors.New("release symbol has no section")
		}
		header, err := sectionBytes(f.Sections[sym.SectionNumber-1], sym.Value, 16)
		if err != nil {
			return "", false, err
		}
		ptr, n := binary.LittleEndian.Uint64(header[:8]), binary.LittleEndian.Uint64(header[8:])
		if n == 0 || n > 64 || ptr < base || ptr-base > math.MaxUint32 {
			return "", false, errors.New("release value is out of range")
		}
		rva := uint32(ptr - base)
		for _, s := range f.Sections {
			if rva >= s.VirtualAddress && rva-s.VirtualAddress < s.VirtualSize {
				value, err := sectionBytes(s, rva-s.VirtualAddress, uint32(n))
				if err != nil {
					return "", false, err
				}
				return string(value), true, nil
			}
		}
		return "", false, errors.New("release value is outside every section")
	}
	return "", false, nil
}

func sectionBytes(s *pe.Section, off, n uint32) ([]byte, error) {
	data, err := s.Data()
	if err != nil {
		return nil, err
	}
	if uint64(off)+uint64(n) > uint64(len(data)) {
		return nil, fmt.Errorf("section %s is too short", s.Name)
	}
	return data[off : off+n], nil
}

// check requires every binary to carry the manifest's build identity and
// the release value; the daemon and the helper must link it.
func check(m buildManifest, release string, binaries map[string]executable, helperSum string, development bool) (Identity, error) {
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
	for name, b := range binaries {
		if b.info == nil || b.info.GoVersion != m.Go {
			return Identity{}, fmt.Errorf("%s was not built with %s", name, m.Go)
		}
		got := map[string]string{}
		for _, s := range b.info.Settings {
			got[s.Key] = s.Value
		}
		for key, value := range want {
			if got[key] != value {
				return Identity{}, fmt.Errorf("%s has %s %q, want %q", name, key, got[key], value)
			}
		}
		switch {
		case b.linked && b.release != release:
			return Identity{}, fmt.Errorf("%s links release %q, not %s", name, b.release, release)
		case !b.linked && b.needsRelease:
			return Identity{}, fmt.Errorf("%s does not link a release value", name)
		}
	}
	return Identity{Release: release, Commit: m.Commit, Modified: m.Dirty, Go: m.Go, Target: m.GOOS + "/" + m.GOARCH,
		Helper: helperSum, Admissible: !m.Dirty}, nil
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
