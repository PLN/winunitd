package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Mutable data directories under the machine data root. They are permanent
// MSI directory components and must not contain file components. Repair
// does not reapply ACLs on these directories and must not follow a reparse
// point into them. Packaging does not rewrite the files inside: unit text,
// enable records, journals, timer state, and linger records stay as they
// are. A format change needs an explicit migration. MSI rollback does not
// undo an application-data migration.
var mutableDataDirs = []string{"units", "enabled", "journal", "runtime", "linger", "daemon"}

const (
	modeInstall   = "install"
	modeRepair    = "repair"
	modeUpgrade   = "upgrade"
	modeUninstall = "uninstall"
)

const (
	maxTaskFiles = 4096
	maxTaskDepth = 8
	maxTaskBytes = 256 * 1024
)

// serviceFacts is the machine-wide SCM observation for the winunitd service.
// Exists false means the service is absent. DecomposeOK means the image
// path is exactly the package form: executable, --base-dir, directory.
type serviceFacts struct {
	Exists      bool
	DecomposeOK bool
	Binary      string
	BaseDir     string
	LocalSystem bool
}

// dirFact describes one directory without opening a reparse target.
type dirFact struct {
	Name        string
	Missing     bool
	Reparse     bool
	Directory   bool
	TrustedOwn  bool
	Restrictive bool
}

var msiGUID = regexp.MustCompile(`(?i)^\{?[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\}?$`)

// preflightMode classifies the MSI properties passed into service-prepare.
// REMOVE=ALL is uninstall even when Installed is also set. An upgrade
// detection wins over a plain Installed value. Any other non-empty
// Installed or feature-remove value is maintenance of this package.
// MSI sets Installed to an installation date/time, not a product code, on
// repair, same-package reinstall, and uninstall.
func preflightMode(installed, upgrade, remove string) (string, error) {
	if !validInstalledToken(installed) || !validGUIDList(upgrade) || !validRemoveToken(remove) {
		return "", fmt.Errorf("preflight conflict: invalid installer context")
	}
	if strings.EqualFold(strings.TrimSpace(remove), "ALL") {
		return modeUninstall, nil
	}
	if strings.TrimSpace(upgrade) != "" {
		return modeUpgrade, nil
	}
	if strings.TrimSpace(installed) != "" || strings.TrimSpace(remove) != "" {
		return modeRepair, nil
	}
	return modeInstall, nil
}

// validInstalledToken accepts the Installed property. Empty is a first
// install. A product-code list is still maintenance. Windows Installer
// itself writes a date: HH:MM:SS (observed as 00:00:00) or 14-digit
// YYYYMMDDHHMMSS (for example 20260923000000).
func validInstalledToken(raw string) bool {
	if validGUIDList(raw) {
		return true
	}
	return msiInstalledDate(strings.TrimSpace(raw))
}

// msiInstalledDate reports whether raw is a Windows Installer date/time.
// The clock form is HH:MM:SS. The other form is YYYYMMDDHHMMSS.
func msiInstalledDate(raw string) bool {
	switch len(raw) {
	case 8:
		if raw[2] != ':' || raw[5] != ':' {
			return false
		}
		return decimalField(raw[0:2], 0, 23) &&
			decimalField(raw[3:5], 0, 59) &&
			decimalField(raw[6:8], 0, 59)
	case 14:
		return decimalField(raw[0:4], 1980, 9999) &&
			decimalField(raw[4:6], 1, 12) &&
			decimalField(raw[6:8], 1, 31) &&
			decimalField(raw[8:10], 0, 23) &&
			decimalField(raw[10:12], 0, 59) &&
			decimalField(raw[12:14], 0, 59)
	default:
		return false
	}
}

func decimalField(s string, min, max int) bool {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n >= min && n <= max
}

func validGUIDList(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !msiGUID.MatchString(part) {
			return false
		}
	}
	return true
}

func validRemoveToken(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "ALL") {
		return true
	}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for _, c := range part {
			if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return false
			}
		}
	}
	return true
}

// evaluatePreflight fails closed before service start or ownership changes.
// Directory problems are reported before service identity, and service
// identity before an incompatible pilot. Uninstall does not treat a pilot
// launcher as a blocker; it still refuses an unrelated service and an
// unsafe directory.
func evaluatePreflight(mode string, dirs []dirFact, service serviceFacts, installDir, dataDir string, pilots int) error {
	for _, dir := range dirs {
		if err := classifyDirectory(dir); err != nil {
			return err
		}
	}
	if err := classifyService(mode, service, installDir, dataDir); err != nil {
		return err
	}
	return classifyPilot(mode, pilots)
}

func classifyDirectory(f dirFact) error {
	name := safeDirName(f.Name)
	if f.Missing {
		return nil
	}
	if f.Reparse {
		return fmt.Errorf("preflight conflict: unsafe directory %s is a reparse point; refusing to follow it", name)
	}
	if !f.Directory {
		return fmt.Errorf("preflight conflict: unsafe directory %s is not an ordinary directory", name)
	}
	if !f.TrustedOwn {
		return fmt.Errorf("preflight conflict: unsafe directory %s has unexpected ownership", name)
	}
	if !f.Restrictive {
		return fmt.Errorf("preflight conflict: unsafe directory %s permits non-administrator writes", name)
	}
	return nil
}

func safeDirName(name string) string {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return "directory"
	}
	return name
}

func classifyService(mode string, facts serviceFacts, installDir, dataDir string) error {
	if !facts.Exists {
		return nil
	}
	// The helper runs on Windows. Compare with Windows path rules so a
	// trailing "\." from the MSI directory property matches the standard
	// data directory in tests on any host.
	expectedExe := windowsJoin(installDir, "bin", "winunitd.exe")
	binaryOK := facts.DecomposeOK && equalWindowsPath(facts.Binary, expectedExe)
	baseOK := facts.DecomposeOK && equalWindowsPath(facts.BaseDir, dataDir)
	if !binaryOK {
		return fmt.Errorf("preflight conflict: unmanaged winunitd binary path")
	}
	if !baseOK {
		return fmt.Errorf("preflight conflict: custom base directory; refusing to adopt or relocate it; explicit migration is required")
	}
	if !facts.LocalSystem {
		return fmt.Errorf("preflight conflict: unrelated pre-existing winunitd service")
	}
	if mode == modeInstall {
		return fmt.Errorf("preflight conflict: pre-existing winunitd service is not owned by this package")
	}
	return nil
}

func equalWindowsPath(a, b string) bool {
	return strings.EqualFold(cleanWindowsPath(a), cleanWindowsPath(b))
}

func windowsJoin(elem ...string) string {
	return cleanWindowsPath(strings.Join(elem, `\`))
}

// cleanWindowsPath applies Windows Clean rules without using the host
// separator. Drive-letter paths and a trailing dot segment are enough
// for the package directories.
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

func classifyPilot(mode string, launchers int) error {
	if mode == modeUninstall || launchers == 0 {
		return nil
	}
	if launchers < 0 {
		return fmt.Errorf("preflight conflict: could not inspect machine launchers")
	}
	return fmt.Errorf("preflight conflict: incompatible pilot launches winunitd; explicit migration is required")
}

func mentionsWinunitdExe(data []byte) bool {
	return strings.Contains(strings.ToLower(string(data)), "winunitd.exe")
}

func countRunLaunchers(values []string) int {
	n := 0
	for _, value := range values {
		if mentionsWinunitdExe([]byte(value)) {
			n++
		}
	}
	return n
}

// countTaskLaunchers counts machine scheduled-task definitions whose
// command mentions winunitd.exe. Reparse points are rejected and not
// opened. The walk is bounded and fails closed when the bound is hit.
func countTaskLaunchers(root string) (int, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return 0, fmt.Errorf("preflight conflict: could not inspect machine launchers")
	}
	reparse, err := pathIsReparse(root, info)
	if err != nil {
		return 0, fmt.Errorf("preflight conflict: could not inspect machine launchers")
	}
	if reparse || !info.IsDir() {
		return 0, fmt.Errorf("preflight conflict: unsafe directory launchers is a reparse point; refusing to follow it")
	}
	count := 0
	files := 0
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if depth > maxTaskDepth {
			return fmt.Errorf("preflight conflict: machine launcher inspection exceeded its bound")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("preflight conflict: could not inspect machine launchers")
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			info, err := os.Lstat(path)
			if err != nil {
				return fmt.Errorf("preflight conflict: could not inspect machine launchers")
			}
			reparse, err := pathIsReparse(path, info)
			if err != nil {
				return fmt.Errorf("preflight conflict: could not inspect machine launchers")
			}
			if reparse {
				return fmt.Errorf("preflight conflict: unsafe directory launchers is a reparse point; refusing to follow it")
			}
			if info.IsDir() {
				if err := walk(path, depth+1); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() {
				continue
			}
			files++
			if files > maxTaskFiles || info.Size() > maxTaskBytes {
				return fmt.Errorf("preflight conflict: machine launcher inspection exceeded its bound")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("preflight conflict: could not inspect machine launchers")
			}
			if mentionsWinunitdExe(data) {
				count++
			}
		}
		return nil
	}
	if err := walk(root, 0); err != nil {
		return 0, err
	}
	return count, nil
}
