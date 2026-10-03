package headless

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

// The admitted run manifest has the same format as the nested-job lane's, so
// one manifest admits both lanes of a combined qualification.

// AdmissionSchema is the admitted run manifest schema.
const AdmissionSchema = 1

// MaxFileBytes bounds every JSON file this package reads. A record carries
// its observer report, up to MaxGenerations processes.
const MaxFileBytes = 8 << 20

// Admission is the controller's reviewed run manifest: the admitted clean
// source and the SHA-256 of every executable a run may use.
type Admission struct {
	Schema    int        `json:"schema"`
	Source    string     `json:"source"`
	Dirty     bool       `json:"dirty"`
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact is one admitted executable.
type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// AdmittedRun is a loaded admission manifest and the hash of its bytes.
type AdmittedRun struct {
	Manifest *Admission
	Hash     string
}

var (
	fullCommit   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Hex    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	artifactName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

// Admits reports whether an executable hash is admitted.
func (a *Admission) Admits(sum string) bool {
	if a == nil {
		return false
	}
	for _, art := range a.Artifacts {
		if art.SHA256 == sum {
			return true
		}
	}
	return false
}

// Lookup returns the admitted SHA-256 of the named artifact, or "" when the
// manifest has no such artifact. A proof that names a role, such as the
// recorder or a test binary, must match that entry, not merely any admitted
// executable.
func (a *Admission) Lookup(name string) string {
	if a == nil {
		return ""
	}
	for _, art := range a.Artifacts {
		if art.Name == name {
			return art.SHA256
		}
	}
	return ""
}

func (a *Admission) validate() error {
	if a.Schema != AdmissionSchema {
		return fmt.Errorf("admission schema %d", a.Schema)
	}
	if !fullCommit.MatchString(a.Source) || a.Dirty {
		return errors.New("admission needs a full clean source commit")
	}
	if len(a.Artifacts) == 0 || len(a.Artifacts) > 64 {
		return errors.New("admission needs 1 to 64 artifacts")
	}
	seen := map[string]bool{}
	for _, art := range a.Artifacts {
		if !artifactName.MatchString(art.Name) || !sha256Hex.MatchString(art.SHA256) || seen[art.Name] {
			return fmt.Errorf("admission artifact %q", art.Name)
		}
		seen[art.Name] = true
	}
	return nil
}

// DecodeAdmission strictly decodes a manifest and returns it with the
// SHA-256 of its bytes.
func DecodeAdmission(data []byte) (AdmittedRun, error) {
	if len(data) > MaxFileBytes {
		return AdmittedRun{}, errors.New("admission manifest is too large")
	}
	var a Admission
	if err := decodeStrict(data, &a); err != nil {
		return AdmittedRun{}, fmt.Errorf("admission manifest: %w", err)
	}
	if err := a.validate(); err != nil {
		return AdmittedRun{}, err
	}
	sum := sha256.Sum256(data)
	return AdmittedRun{Manifest: &a, Hash: hex.EncodeToString(sum[:])}, nil
}

// LoadAdmission reads and decodes an admitted run manifest.
func LoadAdmission(path string) (AdmittedRun, error) {
	data, err := readBounded(path)
	if err != nil {
		return AdmittedRun{}, err
	}
	return DecodeAdmission(data)
}

// readBounded reads a file of at most MaxFileBytes. Its errors name the
// file, never its directory.
func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, baseOnly(err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, baseOnly(err)
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), MaxFileBytes)
	}
	return data, nil
}

// FileSHA256 hashes one file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", baseOnly(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", baseOnly(err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// baseOnly keeps a file error's operation, file name and cause but drops
// its directory, which can name an account's profile or a private share:
// diagnostics in ordinary test and command output stay free of them.
func baseOnly(err error) error {
	var pe *fs.PathError
	var le *os.LinkError
	var ee *exec.Error
	switch {
	case errors.As(err, &pe):
		return &fs.PathError{Op: pe.Op, Path: filepath.Base(pe.Path), Err: pe.Err}
	case errors.As(err, &le):
		return &os.LinkError{Op: le.Op, Old: filepath.Base(le.Old), New: filepath.Base(le.New), Err: le.Err}
	case errors.As(err, &ee):
		return &exec.Error{Name: filepath.Base(ee.Name), Err: ee.Err}
	}
	return err
}

// ExecutableSHA256 hashes the running executable.
func ExecutableSHA256() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return FileSHA256(exe)
}

// boundedBuffer keeps at most MaxFileBytes and notes what it dropped.
type boundedBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := MaxFileBytes - b.Len(); len(p) > room {
		b.truncated = true
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
