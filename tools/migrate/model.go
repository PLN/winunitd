package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/PLN/winunitd/internal/version"
)

// errReparse means a fixture path is a symlink. The tool does not follow it.
var errReparse = errors.New("reparse")

const (
	fixtureFile     = "fixture.json"
	backupKind      = "winunitd-migrate-backup"
	productBinary   = `%ProgramFiles%\winunitd\bin\winunitd.exe`
	standardBase    = `%ProgramData%\winunitd`
	serviceName     = "winunitd"
	controlOwnerRel = "runtime/control-owner"
	controlOwner    = "msi-service\n"
	maxTreeFiles    = 4096
	maxFileBytes    = 256 * 1024
	maxTextBytes    = 64 * 1024
)

// fixture is the disposable pilot stand-in. Paths inside it are relative.
// Live Hermes layout, account secrets, and private host names do not belong
// in this file.
type fixture struct {
	Service         serviceState `json:"service"`
	BetaUpgradeCode string       `json:"beta_upgrade_code,omitempty"`
	DataReparse     bool         `json:"data_reparse"`
	DataUnsafe      bool         `json:"data_unsafe"`
	MachineTasks    []taskState  `json:"machine_tasks"`
	MachineRun      []runValue   `json:"machine_run"`
	Users           []userState  `json:"users"`
}

type serviceState struct {
	Exists      bool   `json:"exists"`
	Binary      string `json:"binary,omitempty"`
	BaseDir     string `json:"base_dir,omitempty"`
	LocalSystem bool   `json:"local_system"`
	Running     bool   `json:"running"`
}

type taskState struct {
	Name       string `json:"name"`
	Command    string `json:"command"`
	Enabled    bool   `json:"enabled"`
	Manager    bool   `json:"manager"`
	Definition string `json:"definition"`
}

type runValue struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

type userState struct {
	Name        string         `json:"name"`
	Tasks       []taskState    `json:"tasks"`
	Processes   []processState `json:"processes"`
	LingerOptIn bool           `json:"linger_opt_in"`
	PilotRel    string         `json:"pilot_rel"`
	DestRel     string         `json:"dest_rel"`
}

type processState struct {
	Command string `json:"command"`
	Running bool   `json:"running"`
}

type fileRec struct {
	Rel    string `json:"rel"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Kind          string    `json:"kind"`
	FixtureSHA256 string    `json:"fixture_sha256"`
	Files         []fileRec `json:"files"`
}

func loadFixture(root string) (fixture, error) {
	raw, err := os.ReadFile(filepath.Join(root, fixtureFile))
	if err != nil {
		return fixture{}, fmt.Errorf("read fixture")
	}
	if bytes.Contains(bytes.ToUpper(raw), []byte("S-1-5-")) {
		return fixture{}, fmt.Errorf("fixture contains a private account marker")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var fx fixture
	if err := dec.Decode(&fx); err != nil {
		return fixture{}, fmt.Errorf("invalid fixture")
	}
	if err := validateFixture(fx); err != nil {
		return fx, err
	}
	return fx, nil
}

func saveFixture(root string, fx fixture) error {
	if err := validateFixture(fx); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if bytes.Contains(bytes.ToUpper(raw), []byte("S-1-5-")) {
		return fmt.Errorf("fixture contains a private account marker")
	}
	return os.WriteFile(filepath.Join(root, fixtureFile), raw, 0o644)
}

func validateFixture(fx fixture) error {
	if err := validateTasks("machine", fx.MachineTasks); err != nil {
		return err
	}
	for _, run := range fx.MachineRun {
		if err := validateName(run.Name); err != nil {
			return err
		}
		if err := validateText("command", run.Command); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, user := range fx.Users {
		if !fixtureAccount(user.Name) {
			return fmt.Errorf("fixture account must be alice, bob, or carol")
		}
		if seen[user.Name] {
			return fmt.Errorf("duplicate fixture account")
		}
		seen[user.Name] = true
		if err := validateRel(user.PilotRel); err != nil {
			return fmt.Errorf("pilot path must stay inside the fixture")
		}
		if err := validateRel(user.DestRel); err != nil {
			return fmt.Errorf("destination path must stay inside the fixture")
		}
		if err := validateTasks(user.Name, user.Tasks); err != nil {
			return err
		}
		for _, proc := range user.Processes {
			if err := validateText("command", proc.Command); err != nil || strings.TrimSpace(proc.Command) == "" {
				return fmt.Errorf("owned process command is required")
			}
		}
	}
	if fx.BetaUpgradeCode != "" {
		if !sameGUID(fx.BetaUpgradeCode, version.BetaUpgradeCode) && !sameGUID(fx.BetaUpgradeCode, version.UpgradeCode) {
			return fmt.Errorf("fixture upgrade code is not a known product identity")
		}
	}
	return nil
}

func validateTasks(scope string, tasks []taskState) error {
	seen := map[string]bool{}
	for _, task := range tasks {
		if err := validateName(task.Name); err != nil {
			return err
		}
		if seen[task.Name] {
			return fmt.Errorf("duplicate task name")
		}
		seen[task.Name] = true
		if err := validateText("command", task.Command); err != nil || strings.TrimSpace(task.Command) == "" {
			return fmt.Errorf("task command is required")
		}
		if err := validateText("definition", task.Definition); err != nil || strings.TrimSpace(task.Definition) == "" {
			return fmt.Errorf("task definition is required")
		}
		if task.Manager && !launchesWinunitd(task.Command) {
			return fmt.Errorf("manager task %s/%s does not launch winunitd", scope, task.Name)
		}
	}
	return nil
}

func validateName(name string) error {
	if len(name) == 0 || len(name) > 64 {
		return fmt.Errorf("invalid fixture name")
	}
	for i, c := range name {
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || (i > 0 && c == '-')
		if !ok {
			return fmt.Errorf("invalid fixture name")
		}
	}
	return nil
}

func validateText(kind, value string) error {
	if len(value) > maxTextBytes {
		return fmt.Errorf("%s exceeds its bound", kind)
	}
	if strings.Contains(strings.ToUpper(value), "S-1-5-") {
		return fmt.Errorf("fixture contains a private account marker")
	}
	return nil
}

func validateRel(rel string) error {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, ":") {
		return fmt.Errorf("path escapes the fixture")
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, ":") {
		return fmt.Errorf("path escapes the fixture")
	}
	return nil
}

func fixtureAccount(name string) bool {
	switch name {
	case "alice", "bob", "carol":
		return true
	default:
		return false
	}
}

func sameGUID(a, b string) bool {
	trim := func(s string) string {
		return strings.Trim(strings.ToUpper(strings.TrimSpace(s)), "{}")
	}
	return trim(a) == trim(b)
}

func launchesWinunitd(command string) bool {
	return strings.Contains(strings.ToLower(command), "winunitd.exe")
}

func serviceManaged(s serviceState) bool {
	if !s.Exists || !s.LocalSystem {
		return false
	}
	return equalWindowsPath(s.Binary, productBinary) && equalWindowsPath(s.BaseDir, standardBase)
}

func equalWindowsPath(a, b string) bool {
	return strings.EqualFold(cleanWindowsPath(a), cleanWindowsPath(b))
}

func cleanWindowsPath(p string) string {
	p = strings.ReplaceAll(p, "/", `\`)
	vol := ""
	rest := p
	if len(p) >= 2 && p[1] == ':' {
		vol = p[:2]
		rest = p[2:]
	}
	var stack []string
	for _, part := range strings.Split(rest, `\`) {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			stack = append(stack, part)
		}
	}
	cleaned := strings.Join(stack, `\`)
	if vol != "" {
		if cleaned == "" {
			return vol + `\`
		}
		return vol + `\` + cleaned
	}
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, `//`) {
		return `\\` + cleaned
	}
	return cleaned
}

func fixtureBytes(root string) ([]byte, error) {
	return os.ReadFile(filepath.Join(root, fixtureFile))
}

func sha256Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read pilot file")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errReparse
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("fixture tree exceeds its bound")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pilot file")
	}
	if bytes.Contains(bytes.ToUpper(raw), []byte("S-1-5-")) {
		return nil, fmt.Errorf("fixture contains a private account marker")
	}
	return raw, nil
}

func sha256File(path string) (string, error) {
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

// walkFiles lists regular files under abs. Symlinks are conflicts.
// rel values use forward slashes and are relative to root.
func walkFiles(root, abs string) ([]string, error) {
	if abs == "" {
		return nil, nil
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect fixture tree")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errReparse
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("fixture tree is not a directory")
	}
	var out []string
	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("inspect fixture tree")
		}
		if path == abs {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect fixture tree")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errReparse
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("fixture tree has an unexpected file type")
		}
		if info.Size() > maxFileBytes {
			return fmt.Errorf("fixture tree exceeds its bound")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("fixture path escaped")
		}
		rel = filepath.ToSlash(rel)
		if err := validateRel(rel); err != nil {
			return err
		}
		out = append(out, rel)
		if len(out) > maxTreeFiles {
			return fmt.Errorf("fixture tree exceeds its bound")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("inspect fixture tree")
		}
		if bytes.Contains(bytes.ToUpper(raw), []byte("S-1-5-")) {
			return fmt.Errorf("fixture contains a private account marker")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
