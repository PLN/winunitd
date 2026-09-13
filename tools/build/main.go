// Command build creates pinned, path-trimmed binaries and a public-safe manifest.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/PLN/winunitd/internal/version"
)

type artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

type manifest struct {
	Version        string     `json:"version"`
	Schema         int        `json:"schema"`
	Commit         string     `json:"commit"`
	Dirty          bool       `json:"dirty"`
	Go             string     `json:"go"`
	GOOS           string     `json:"goos"`
	GOARCH         string     `json:"goarch"`
	CGO            bool       `json:"cgo"`
	GoModHash      string     `json:"go_mod_sha256"`
	GoSumHash      string     `json:"go_sum_sha256"`
	ThirdPartyHash string     `json:"third_party_sha256"`
	Artifacts      []artifact `json:"artifacts"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "dist", "output directory")
	goos := flag.String("goos", "windows", "target OS")
	arch := flag.String("goarch", "amd64", "target architecture")
	releaseVersion := flag.String("version", version.Version, "binary release version")
	flag.Parse()
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`).MatchString(*releaseVersion) {
		return fmt.Errorf("invalid release version")
	}
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	pin, err := os.ReadFile(".go-version")
	if err != nil {
		return fmt.Errorf("run from the repository root: %w", err)
	}
	want := "go" + strings.TrimSpace(string(pin))
	if runtime.Version() != want {
		return fmt.Errorf("requires %s, running %s; set GOTOOLCHAIN=%s", want, runtime.Version(), want)
	}
	goexe, err := pinnedGo(want, queryGoEnvironment)
	if err != nil {
		return err
	}
	commit, err := commandOutput("git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	status, err := commandOutput("git", "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	modHash, err := hashFile("go.mod")
	if err != nil {
		return err
	}
	sumHash, err := hashFile("go.sum")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	// Never leave a stale manifest looking like a successful new build.
	manifestPath := filepath.Join(*out, "build-manifest.json")
	if err := os.Remove(manifestPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	thirdPartyHash, err := hashTree("third_party/go-winio")
	if err != nil {
		return err
	}
	m := manifest{Schema: 2, Version: *releaseVersion, ThirdPartyHash: thirdPartyHash, Commit: commit, Dirty: status != "", Go: want,
		GOOS: *goos, GOARCH: *arch, GoModHash: modHash, GoSumHash: sumHash}
	for _, name := range []string{"winunitd", "winctl", "winunit-notify"} {
		file := name
		if *goos == "windows" {
			file += ".exe"
		}
		path := filepath.Join(*out, file)
		cmd := exec.Command(goexe, "build", "-trimpath", "-buildvcs=true", "-ldflags", "-X github.com/PLN/winunitd/internal/version.Version="+*releaseVersion, "-o", path, "./cmd/"+name)
		cmd.Env = buildEnv(os.Environ(), *goos, *arch)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		m.Artifacts = append(m.Artifacts, artifact{Name: file, SHA256: digest(data), Size: len(data)})
	}

	notice, err := os.ReadFile("third_party/go-winio/LICENSE")
	if err != nil {
		return err
	}
	notice = append([]byte("github.com/Microsoft/go-winio v0.6.2 (locally patched)\n\n"), notice...)
	if err := os.WriteFile(filepath.Join(*out, "THIRD-PARTY-NOTICES.txt"), notice, 0o644); err != nil {
		return err
	}
	m.Artifacts = append(m.Artifacts, artifact{Name: "THIRD-PARTY-NOTICES.txt", SHA256: digest(notice), Size: len(notice)})
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("Built %d artifacts for %s/%s with %s (dirty=%t)\n", len(m.Artifacts), m.GOOS, m.GOARCH, m.Go, m.Dirty)
	return nil
}

func commandOutput(name string, args ...string) (string, error) {
	data, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(data)), nil
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return digest(data), nil
}

func digest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func buildEnv(env []string, goos, arch string) []string {
	var result []string
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		switch strings.ToUpper(key) {
		case "GOOS", "GOARCH", "CGO_ENABLED", "GOTOOLCHAIN", "GOROOT", "GOFLAGS", "GOEXPERIMENT", "GOAMD64", "GOARM64", "GOWORK":
			continue
		}
		result = append(result, e)
	}
	return append(result, "GOOS="+goos, "GOARCH="+arch, "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOFLAGS=", "GOEXPERIMENT=", "GOAMD64=v1", "GOARM64=v8.0", "GOWORK=off")
}

// WalkDir sorts names; slash paths and content hashes make this host-independent.
func hashTree(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular dependency file")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum, err := hashFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%s\n", filepath.ToSlash(rel), sum)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
