package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PLN/winunitd/internal/version"
)

// report is the discover and dry-run record. Paths are fixture-relative.
type report struct {
	Command   string    `json:"command"`
	Blocked   bool      `json:"blocked"`
	Preflight []finding `json:"msi_preflight_rejects"`
	UserMoves []finding `json:"user_context_moves"`
	Conflicts []finding `json:"conflicts"`
	Actions   []action  `json:"actions,omitempty"`
	Health    []string  `json:"health,omitempty"`
	Restored  bool      `json:"restored,omitempty"`
}

type finding struct {
	Code    string `json:"code"`
	Scope   string `json:"scope"`
	Account string `json:"account,omitempty"`
	Detail  string `json:"detail"`
}

type action struct {
	Op      string `json:"op"`
	Account string `json:"account,omitempty"`
	Rel     string `json:"rel,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type options struct {
	UserContext bool
	Linger      bool
	MSIPath     string
	ExecuteMSI  bool
	FailAfter   string
	BackupDir   string
}

func newReport(command string) report {
	return report{
		Command:   command,
		Preflight: []finding{},
		UserMoves: []finding{},
		Conflicts: []finding{},
		Actions:   []action{},
	}
}

func (r *report) addPreflight(code, detail string) {
	f := finding{Code: code, Scope: "machine", Detail: detail}
	r.Preflight = append(r.Preflight, f)
	r.Conflicts = append(r.Conflicts, f)
	r.Blocked = true
}

func (r *report) addConflict(code, scope, account, detail string) {
	r.Conflicts = append(r.Conflicts, finding{Code: code, Scope: scope, Account: account, Detail: detail})
	r.Blocked = true
}

func (r *report) addMove(code, account, detail string) {
	r.UserMoves = append(r.UserMoves, finding{Code: code, Scope: "user", Account: account, Detail: detail})
}

// plan classifies MSI-preflight rejects separately from per-user moves.
// Conflicts block apply. Planning reads the fixture and does not mutate it.
func plan(root string, fx fixture, opt options, includeActions bool) (report, error) {
	rep := newReport("discover")
	if includeActions {
		rep.Command = "dry-run"
	}
	if fx.DataReparse {
		rep.addPreflight("reparse", "machine data directory is a reparse point")
	}
	if fx.DataUnsafe {
		rep.addPreflight("unsafe-data-dir", "machine data directory ownership is unsafe")
	}
	classifyService(&rep, fx.Service)
	if fx.BetaUpgradeCode != "" && sameGUID(fx.BetaUpgradeCode, version.BetaUpgradeCode) {
		rep.addPreflight("beta-upgrade-code", "beta UpgradeCode is still present")
	}
	for _, task := range fx.MachineTasks {
		if task.Enabled && launchesWinunitd(task.Command) {
			rep.addPreflight("machine-launcher", "a machine scheduled task launches winunitd.exe")
		}
	}
	for _, run := range fx.MachineRun {
		if launchesWinunitd(run.Command) {
			rep.addPreflight("machine-launcher", "a machine Run value launches winunitd.exe")
		}
	}

	needsUser := false
	for _, user := range fx.Users {
		work, err := userHasWork(root, user)
		if err != nil {
			if errors.Is(err, errReparse) {
				rep.addConflict("reparse", "user", user.Name, "pilot tree is a reparse point")
				continue
			}
			return rep, err
		}
		if work {
			needsUser = true
		}
		if !opt.UserContext {
			continue
		}
		if err := classifyUser(root, &rep, user, opt, includeActions); err != nil {
			return rep, err
		}
	}
	if needsUser && !opt.UserContext {
		rep.addMove("user-context-required", "", "per-user discovery was not requested")
		rep.addConflict("user-context-required", "user", "", "per-user discovery is required before handoff")
	}
	if includeActions && !rep.Blocked {
		if err := appendMachineActions(&rep, fx, opt); err != nil {
			return rep, err
		}
	}
	if !includeActions {
		rep.Actions = nil
	}
	return rep, nil
}

func classifyService(rep *report, svc serviceState) {
	if !svc.Exists {
		return
	}
	if !svc.LocalSystem {
		rep.addPreflight("non-localsystem", "winunitd service account is not LocalSystem")
	}
	if !equalWindowsPath(svc.Binary, productBinary) {
		rep.addPreflight("unmanaged-service", "unmanaged winunitd binary is not overwritten")
	}
	if !equalWindowsPath(svc.BaseDir, standardBase) {
		rep.addPreflight("custom-base-dir", "custom --base-dir is not adopted")
	}
}

func userHasWork(root string, user userState) (bool, error) {
	if len(user.Tasks) > 0 || len(user.Processes) > 0 {
		return true, nil
	}
	files, err := walkFiles(root, filepath.Join(root, filepath.FromSlash(user.PilotRel)))
	if err != nil {
		return false, err
	}
	return len(files) > 0, nil
}

func classifyUser(root string, rep *report, user userState, opt options, includeActions bool) error {
	launchers := 0
	for _, task := range user.Tasks {
		if task.Enabled && launchesWinunitd(task.Command) {
			launchers++
		}
	}
	if launchers > 1 {
		rep.addConflict("duplicate-launchers", "user", user.Name, "more than one enabled task launches winunitd.exe")
	}
	if len(user.Tasks) > 0 {
		rep.addMove("disable-pilot", user.Name, "disable the pilot manager task and old triggers")
		if includeActions && !rep.Blocked {
			for _, task := range user.Tasks {
				if task.Enabled {
					rep.Actions = append(rep.Actions, action{Op: "disable-task", Account: user.Name, Detail: task.Name})
				}
			}
		}
	}
	runningOwned := 0
	for _, proc := range user.Processes {
		if proc.Running {
			runningOwned++
		}
	}
	if runningOwned > 0 {
		rep.addMove("stop-owned", user.Name, "stop processes owned by the pilot")
		if includeActions && !rep.Blocked {
			for i := 0; i < runningOwned; i++ {
				rep.Actions = append(rep.Actions, action{Op: "stop-process", Account: user.Name, Detail: "owned-process"})
			}
		}
	}
	if err := classifyTree(root, rep, user, opt, includeActions); err != nil {
		return err
	}
	return nil
}

func classifyTree(root string, rep *report, user userState, opt options, includeActions bool) error {
	pilot := filepath.Join(root, filepath.FromSlash(user.PilotRel))
	for _, sub := range []string{"units", "enabled"} {
		files, err := walkFiles(root, filepath.Join(pilot, sub))
		if err != nil {
			if errors.Is(err, errReparse) {
				rep.addConflict("reparse", "user", user.Name, "pilot tree is a reparse point")
				return nil
			}
			return err
		}
		op := "copy-unit"
		if sub == "enabled" {
			op = "copy-enable"
		}
		if len(files) > 0 {
			rep.addMove(op, user.Name, "copy into the standard user data tree")
		}
		for _, rel := range files {
			target, err := destRelFor(user, rel)
			if err != nil {
				return err
			}
			state, err := compareCopy(root, rel, target)
			if err != nil {
				if errors.Is(err, errReparse) {
					rep.addConflict("reparse", "user", user.Name, "destination tree is a reparse point")
					return nil
				}
				return err
			}
			if state == destConflict {
				rep.addConflict("destination-collision", "user", user.Name, "destination file differs")
				continue
			}
			if includeActions && !rep.Blocked {
				detail := "copy"
				if state == destSame {
					detail = "unchanged"
				}
				rep.Actions = append(rep.Actions, action{Op: op, Account: user.Name, Rel: target, Detail: detail})
			}
		}
	}
	journal, err := walkFiles(root, filepath.Join(pilot, "journal"))
	if err != nil {
		if errors.Is(err, errReparse) {
			rep.addConflict("reparse", "user", user.Name, "journal tree is a reparse point")
			return nil
		}
		return err
	}
	for _, rel := range journal {
		rep.addMove("archive-journal", user.Name, "journal stays in place and is archived without overwrite")
		if includeActions && !rep.Blocked {
			rep.Actions = append(rep.Actions, action{Op: "archive-journal", Account: user.Name, Rel: rel, Detail: "backup-only"})
		}
	}
	lingerFiles, err := walkFiles(root, filepath.Join(pilot, "linger"))
	if err != nil {
		if errors.Is(err, errReparse) {
			rep.addConflict("reparse", "user", user.Name, "linger tree is a reparse point")
			return nil
		}
		return err
	}
	if len(lingerFiles) > 0 && !opt.Linger {
		rep.addMove("leave-linger", user.Name, "linger stays in the pilot tree unless explicitly requested")
		if includeActions && !rep.Blocked {
			rep.Actions = append(rep.Actions, action{Op: "leave-linger", Account: user.Name, Detail: "opt-in-required"})
		}
	}
	if opt.Linger {
		for _, rel := range lingerFiles {
			target, err := destRelFor(user, rel)
			if err != nil {
				return err
			}
			state, err := compareCopy(root, rel, target)
			if err != nil {
				if errors.Is(err, errReparse) {
					rep.addConflict("reparse", "user", user.Name, "destination tree is a reparse point")
					return nil
				}
				return err
			}
			if state == destConflict {
				rep.addConflict("destination-collision", "user", user.Name, "destination linger file differs")
				continue
			}
			if includeActions && !rep.Blocked {
				rep.Actions = append(rep.Actions, action{Op: "copy-linger", Account: user.Name, Rel: target, Detail: "explicit-opt-in"})
			}
		}
	}
	owner := user.DestRel + "/" + controlOwnerRel
	state, err := destStateExact(root, owner, []byte(controlOwner))
	if err != nil {
		if errors.Is(err, errReparse) {
			rep.addConflict("reparse", "user", user.Name, "destination tree is a reparse point")
			return nil
		}
		return err
	}
	if state == destConflict {
		rep.addConflict("destination-collision", "user", user.Name, "control owner marker differs")
	}
	return nil
}

func destRelFor(user userState, srcRel string) (string, error) {
	prefix := user.PilotRel + "/"
	if !strings.HasPrefix(srcRel, prefix) {
		return "", fmt.Errorf("pilot path escaped")
	}
	target := user.DestRel + "/" + strings.TrimPrefix(srcRel, prefix)
	if err := validateRel(target); err != nil {
		return "", err
	}
	return target, nil
}

func compareCopy(root, srcRel, dstRel string) (destCompare, error) {
	body, err := readRegular(filepath.Join(root, filepath.FromSlash(srcRel)))
	if err != nil {
		return 0, err
	}
	return destStateExact(root, dstRel, body)
}

func appendMachineActions(rep *report, fx fixture, opt options) error {
	if serviceNeedsInstall(fx.Service) {
		if strings.TrimSpace(opt.MSIPath) == "" {
			rep.addConflict("msi-required", "machine", "", "caller-supplied MSI path is required")
			return nil
		}
		info, err := os.Lstat(opt.MSIPath)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
			rep.addConflict("msi-required", "machine", "", "caller-supplied MSI path is required")
			return nil
		}
		rep.Actions = append(rep.Actions, action{Op: "install-msi", Detail: "caller-supplied-msi"})
		rep.Actions = append(rep.Actions, action{Op: "start-service", Detail: serviceName})
	} else if fx.Service.Exists && serviceManaged(fx.Service) && !fx.Service.Running {
		rep.Actions = append(rep.Actions, action{Op: "start-service", Detail: serviceName})
	}
	rep.Actions = append(rep.Actions, action{Op: "health-check", Detail: "ownership-and-workload"})
	return nil
}

func serviceNeedsInstall(svc serviceState) bool {
	return !svc.Exists
}

type destCompare int

const (
	destMissing destCompare = iota
	destSame
	destConflict
)

func destStateExact(root, rel string, want []byte) (destCompare, error) {
	if err := validateRel(rel); err != nil {
		return 0, err
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return destMissing, nil
		}
		return 0, fmt.Errorf("inspect destination")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return 0, errReparse
	}
	if !info.Mode().IsRegular() {
		return destConflict, nil
	}
	got, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read destination")
	}
	if bytes.Equal(got, want) {
		return destSame, nil
	}
	return destConflict, nil
}
