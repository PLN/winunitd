package nestedjob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
)

// EnvAdmission names the admitted run manifest of a qualification run.
const EnvAdmission = "WINUNITD_NATIVE_NESTED_ADMISSION"

// AdmissionSchema is the admitted run manifest schema.
const AdmissionSchema = 1

// Admission is the controller's reviewed run manifest: the admitted clean
// source and the SHA-256 of every executable a run may use (daemon, CLIs,
// fixture and test binaries). Records bind to its hash; a summary accepts
// only records made by admitted executables from that source.
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

// Lookup returns the admitted artifact name.
func (a *Admission) Lookup(name string) (Artifact, bool) {
	for _, art := range a.Artifacts {
		if art.Name == name {
			return art, true
		}
	}
	return Artifact{}, false
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
	if len(data) > MaxStatusBytes {
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
	f, err := os.Open(path)
	if err != nil {
		return AdmittedRun{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxStatusBytes+1))
	if err != nil {
		return AdmittedRun{}, err
	}
	return DecodeAdmission(data)
}

// FileSHA256 hashes one file.
func FileSHA256(path string) (string, error) {
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

// ExecutableSHA256 hashes the running executable.
func ExecutableSHA256() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return FileSHA256(exe)
}
