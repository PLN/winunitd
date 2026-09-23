package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var errBlocked = errors.New("migration blocked by conflicts")

func backup(root, out string) error {
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("backup directory is required")
	}
	if err := outsideRoot(root, out); err != nil {
		return err
	}
	if err := emptyDir(out); err != nil {
		return err
	}
	raw, err := fixtureBytes(root)
	if err != nil {
		return fmt.Errorf("read fixture")
	}
	fx, err := loadFixture(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return fmt.Errorf("create backup directory")
	}
	if err := os.WriteFile(filepath.Join(out, fixtureFile), raw, 0o644); err != nil {
		return fmt.Errorf("write backup")
	}
	files := []fileRec{{Rel: fixtureFile, SHA256: sha256Bytes(raw)}}
	for _, user := range fx.Users {
		rels, err := walkFiles(root, filepath.Join(root, filepath.FromSlash(user.PilotRel)))
		if err != nil {
			return err
		}
		for _, rel := range rels {
			sum, err := sha256File(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				return fmt.Errorf("hash fixture file")
			}
			files = append(files, fileRec{Rel: rel, SHA256: sum})
			if err := copyFile(filepath.Join(root, filepath.FromSlash(rel)), filepath.Join(out, "files", filepath.FromSlash(rel))); err != nil {
				return err
			}
		}
		for _, task := range user.Tasks {
			if err := writeNew(filepath.Join(out, "tasks", user.Name, task.Name+".xml"), []byte(task.Definition)); err != nil {
				return err
			}
		}
	}
	for _, task := range fx.MachineTasks {
		if err := writeNew(filepath.Join(out, "tasks", "machine", task.Name+".xml"), []byte(task.Definition)); err != nil {
			return err
		}
	}
	for _, run := range fx.MachineRun {
		if err := writeNew(filepath.Join(out, "run", run.Name+".txt"), []byte(run.Command)); err != nil {
			return err
		}
	}
	man := manifest{Kind: backupKind, FixtureSHA256: sha256Bytes(raw), Files: files}
	body, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "manifest.json"), append(body, '\n'), 0o644)
}

func apply(root string, opt options) error {
	if err := knownPhase(opt.FailAfter); err != nil {
		return err
	}
	if opt.ExecuteMSI {
		if err := executeMSISupported(); err != nil {
			return err
		}
	}
	if strings.TrimSpace(opt.BackupDir) == "" {
		return fmt.Errorf("backup directory is required")
	}
	fx, err := loadFixture(root)
	if err != nil {
		return err
	}
	if err := backupMatches(root, opt.BackupDir); err != nil {
		return err
	}
	rep, err := plan(root, fx, opt, true)
	if err != nil {
		return err
	}
	if rep.Blocked {
		return errBlocked
	}
	if err := writeDestBefore(root, opt.BackupDir, fx); err != nil {
		return err
	}
	if err := mutate(root, &fx, opt); err != nil {
		if rbErr := rollback(root, opt.BackupDir); rbErr != nil {
			return fmt.Errorf("%v; rollback failed", err)
		}
		return err
	}
	return nil
}

func mutate(root string, fx *fixture, opt options) error {
	phaseDisable(fx)
	if err := saveFixture(root, *fx); err != nil {
		return err
	}
	if err := injected(opt.FailAfter, "disable"); err != nil {
		return err
	}
	phaseStop(fx)
	if err := saveFixture(root, *fx); err != nil {
		return err
	}
	if err := injected(opt.FailAfter, "stop"); err != nil {
		return err
	}
	if err := phaseCopy(root, *fx, opt.Linger); err != nil {
		return err
	}
	if err := injected(opt.FailAfter, "copy"); err != nil {
		return err
	}
	if err := phaseInstall(root, fx, opt); err != nil {
		return err
	}
	if err := saveFixture(root, *fx); err != nil {
		return err
	}
	if err := injected(opt.FailAfter, "install"); err != nil {
		return err
	}
	if _, err := evaluateHealth(root, *fx, opt); err != nil {
		return err
	}
	if err := injected(opt.FailAfter, "health"); err != nil {
		return err
	}
	return nil
}

func knownPhase(phase string) error {
	switch phase {
	case "", "disable", "stop", "copy", "install", "health":
		return nil
	default:
		return fmt.Errorf("unknown failure phase")
	}
}

func injected(got, phase string) error {
	if got == phase {
		return fmt.Errorf("injected failure after %s", phase)
	}
	return nil
}

func phaseDisable(fx *fixture) {
	for i := range fx.Users {
		for j := range fx.Users[i].Tasks {
			fx.Users[i].Tasks[j].Enabled = false
		}
	}
}

func phaseStop(fx *fixture) {
	for i := range fx.Users {
		for j := range fx.Users[i].Processes {
			fx.Users[i].Processes[j].Running = false
		}
	}
}

func phaseCopy(root string, fx fixture, linger bool) error {
	subs := []string{"units", "enabled"}
	if linger {
		subs = append(subs, "linger")
	}
	for _, user := range fx.Users {
		pilot := filepath.Join(root, filepath.FromSlash(user.PilotRel))
		for _, sub := range subs {
			rels, err := walkFiles(root, filepath.Join(pilot, sub))
			if err != nil {
				return err
			}
			for _, rel := range rels {
				target, err := destRelFor(user, rel)
				if err != nil {
					return err
				}
				body, err := readRegular(filepath.Join(root, filepath.FromSlash(rel)))
				if err != nil {
					return err
				}
				if err := writeNew(filepath.Join(root, filepath.FromSlash(target)), body); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func phaseInstall(root string, fx *fixture, opt options) error {
	switch {
	case serviceNeedsInstall(fx.Service):
		if err := installCallerMSI(opt); err != nil {
			return err
		}
		fx.Service = serviceState{
			Exists:      true,
			Binary:      productBinary,
			BaseDir:     standardBase,
			LocalSystem: true,
			Running:     true,
		}
		if opt.ExecuteMSI {
			if err := os.WriteFile(filepath.Join(opt.BackupDir, "msi-executed"), []byte("1\n"), 0o644); err != nil {
				return fmt.Errorf("record msi execution")
			}
		}
	case fx.Service.Exists && serviceManaged(fx.Service) && !fx.Service.Running:
		if opt.ExecuteMSI {
			if err := startInstalledService(); err != nil {
				return err
			}
		}
		fx.Service.Running = true
	}
	for _, user := range fx.Users {
		owner := filepath.Join(root, filepath.FromSlash(user.DestRel), filepath.FromSlash(controlOwnerRel))
		if err := writeNew(owner, []byte(controlOwner)); err != nil {
			return err
		}
	}
	return nil
}

func installCallerMSI(opt options) error {
	info, err := os.Lstat(opt.MSIPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("caller-supplied msi is missing")
	}
	if !opt.ExecuteMSI {
		return nil
	}
	return executeMSI(opt.MSIPath)
}

func evaluateHealth(root string, fx fixture, opt options) ([]string, error) {
	if !serviceManaged(fx.Service) || !fx.Service.Running {
		return nil, fmt.Errorf("health check failed: service-identity")
	}
	if msiExecuted(opt.BackupDir) {
		if err := productServiceMatches(); err != nil {
			return nil, fmt.Errorf("health check failed: service-identity")
		}
	}
	for _, user := range fx.Users {
		for _, task := range user.Tasks {
			if task.Enabled {
				return nil, fmt.Errorf("health check failed: pilot-disabled")
			}
		}
		for _, proc := range user.Processes {
			if proc.Running {
				return nil, fmt.Errorf("health check failed: processes-stopped")
			}
		}
	}
	for _, task := range fx.MachineTasks {
		if task.Enabled && launchesWinunitd(task.Command) {
			return nil, fmt.Errorf("health check failed: machine-launchers-clear")
		}
	}
	for _, run := range fx.MachineRun {
		if launchesWinunitd(run.Command) {
			return nil, fmt.Errorf("health check failed: machine-launchers-clear")
		}
	}
	workload := false
	for _, user := range fx.Users {
		if err := healthTree(root, user, opt.Linger); err != nil {
			return nil, err
		}
		owner := filepath.Join(root, filepath.FromSlash(user.DestRel), filepath.FromSlash(controlOwnerRel))
		body, err := readRegular(owner)
		if err != nil || string(body) != controlOwner {
			return nil, fmt.Errorf("health check failed: control-owner")
		}
		unit := filepath.Join(root, filepath.FromSlash(user.PilotRel), "units", "fixture.service")
		info, err := os.Lstat(unit)
		if err == nil && info.Mode().IsRegular() {
			workload = true
		}
	}
	checks := []string{
		"service-identity",
		"pilot-disabled",
		"processes-stopped",
		"machine-launchers-clear",
		"units-copied",
		"control-owner",
		"linger-left",
	}
	if workload {
		checks = append(checks, "workload-unit")
	}
	return checks, nil
}

func healthTree(root string, user userState, linger bool) error {
	pilot := filepath.Join(root, filepath.FromSlash(user.PilotRel))
	for _, sub := range []string{"units", "enabled"} {
		rels, err := walkFiles(root, filepath.Join(pilot, sub))
		if err != nil {
			return fmt.Errorf("health check failed: units-copied")
		}
		for _, rel := range rels {
			target, err := destRelFor(user, rel)
			if err != nil {
				return fmt.Errorf("health check failed: units-copied")
			}
			state, err := compareCopy(root, rel, target)
			if err != nil || state != destSame {
				if sub == "units" && strings.HasSuffix(rel, "/fixture.service") {
					return fmt.Errorf("health check failed: workload-unit")
				}
				return fmt.Errorf("health check failed: units-copied")
			}
		}
	}
	rels, err := walkFiles(root, filepath.Join(pilot, "linger"))
	if err != nil {
		return fmt.Errorf("health check failed: linger-left")
	}
	for _, rel := range rels {
		target, err := destRelFor(user, rel)
		if err != nil {
			return fmt.Errorf("health check failed: linger-left")
		}
		if linger {
			state, err := compareCopy(root, rel, target)
			if err != nil || state != destSame {
				return fmt.Errorf("health check failed: linger-left")
			}
			continue
		}
		_, err = os.Lstat(filepath.Join(root, filepath.FromSlash(target)))
		if err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("health check failed: linger-left")
		}
	}
	return nil
}

func rollback(root, backupDir string) error {
	if strings.TrimSpace(backupDir) == "" {
		return fmt.Errorf("backup directory is required")
	}
	if msiExecuted(backupDir) {
		if err := stopInstalledService(); err != nil {
			return fmt.Errorf("stop new service ownership")
		}
	}
	raw, err := os.ReadFile(filepath.Join(backupDir, fixtureFile))
	if err != nil {
		return fmt.Errorf("backup fixture missing")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var original fixture
	if err := dec.Decode(&original); err != nil {
		return fmt.Errorf("backup fixture invalid")
	}
	if err := validateFixture(original); err != nil {
		return err
	}
	before, err := readDestBefore(backupDir)
	if err != nil {
		return err
	}
	if err := removeAdded(root, original, before); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, fixtureFile), raw, 0o644); err != nil {
		return fmt.Errorf("restore fixture")
	}
	return nil
}

func backupMatches(root, backupDir string) error {
	rawMan, err := os.ReadFile(filepath.Join(backupDir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("backup manifest missing")
	}
	dec := json.NewDecoder(bytes.NewReader(rawMan))
	dec.DisallowUnknownFields()
	var man manifest
	if err := dec.Decode(&man); err != nil || man.Kind != backupKind {
		return fmt.Errorf("backup manifest is not a migration backup")
	}
	raw, err := fixtureBytes(root)
	if err != nil {
		return fmt.Errorf("read fixture")
	}
	if sha256Bytes(raw) != man.FixtureSHA256 {
		return fmt.Errorf("fixture changed after backup")
	}
	seen := map[string]bool{}
	for _, rec := range man.Files {
		if err := validateRel(rec.Rel); err != nil {
			return fmt.Errorf("backup manifest path")
		}
		sum, err := sha256File(filepath.Join(root, filepath.FromSlash(rec.Rel)))
		if err != nil || sum != rec.SHA256 {
			return fmt.Errorf("fixture changed after backup")
		}
		seen[rec.Rel] = true
	}
	fx, err := loadFixture(root)
	if err != nil {
		return err
	}
	for _, user := range fx.Users {
		rels, err := walkFiles(root, filepath.Join(root, filepath.FromSlash(user.PilotRel)))
		if err != nil {
			return err
		}
		for _, rel := range rels {
			if !seen[rel] {
				return fmt.Errorf("fixture changed after backup")
			}
		}
	}
	return nil
}

func writeDestBefore(root, backupDir string, fx fixture) error {
	rels := []string{}
	for _, user := range fx.Users {
		files, err := walkFiles(root, filepath.Join(root, filepath.FromSlash(user.DestRel)))
		if err != nil {
			return err
		}
		rels = append(rels, files...)
	}
	body, err := json.MarshalIndent(rels, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(backupDir, "dest-before.json"), append(body, '\n'), 0o644)
}

func readDestBefore(backupDir string) (map[string]bool, error) {
	raw, err := os.ReadFile(filepath.Join(backupDir, "dest-before.json"))
	if err != nil {
		return nil, fmt.Errorf("backup is missing destination snapshot")
	}
	var rels []string
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rels); err != nil {
		return nil, fmt.Errorf("backup destination snapshot is invalid")
	}
	out := map[string]bool{}
	for _, rel := range rels {
		if err := validateRel(rel); err != nil {
			return nil, fmt.Errorf("backup destination snapshot is invalid")
		}
		out[rel] = true
	}
	return out, nil
}

func removeAdded(root string, fx fixture, before map[string]bool) error {
	for _, user := range fx.Users {
		dest := filepath.Join(root, filepath.FromSlash(user.DestRel))
		rels, err := walkFiles(root, dest)
		if err != nil {
			if errors.Is(err, errReparse) {
				return fmt.Errorf("destination tree is a reparse point")
			}
			return err
		}
		for _, rel := range rels {
			if before[rel] {
				continue
			}
			if !strings.HasPrefix(rel, user.DestRel+"/") {
				return fmt.Errorf("destination path escaped")
			}
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove migrated file")
			}
		}
	}
	return nil
}

func msiExecuted(backupDir string) bool {
	if strings.TrimSpace(backupDir) == "" {
		return false
	}
	info, err := os.Lstat(filepath.Join(backupDir, "msi-executed"))
	return err == nil && info.Mode().IsRegular()
}

func outsideRoot(root, out string) error {
	root = filepath.Clean(root)
	out = filepath.Clean(out)
	rel, err := filepath.Rel(root, out)
	if err != nil {
		return fmt.Errorf("backup directory must be outside the fixture")
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" || (!strings.HasPrefix(rel, "../") && rel != "..") {
		return fmt.Errorf("backup directory must be outside the fixture")
	}
	return nil
}

func emptyDir(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("backup directory")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("backup directory is not an empty directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) > 0 {
		return fmt.Errorf("backup directory is not empty")
	}
	return nil
}

func copyFile(src, dst string) error {
	body, err := readRegular(src)
	if err != nil {
		return err
	}
	return writeNew(dst, body)
}

func writeNew(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create destination")
	}
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("destination collision")
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read destination")
		}
		if bytes.Equal(got, body) {
			return nil
		}
		return fmt.Errorf("destination collision")
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("create destination")
	}
	return os.WriteFile(path, body, 0o644)
}

func writeReport(w io.Writer, rep report) error {
	if rep.Preflight == nil {
		rep.Preflight = []finding{}
	}
	if rep.UserMoves == nil {
		rep.UserMoves = []finding{}
	}
	if rep.Conflicts == nil {
		rep.Conflicts = []finding{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
