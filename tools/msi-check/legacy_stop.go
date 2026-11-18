package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
)

// legacyStopProducts are the product packages released before 0.6.1, by
// ProductCode. Their manager seals admission before its own shutdown stop
// pass, and that pass then refuses a fresh ExecStop helper with "manager is
// closing". Maintenance reports the failure, the upgrade aborts, and the
// rollback restart of the old service costs the running units their
// processes. A fixed manager keeps the dedicated maintenance path alone.
var legacyStopProducts = map[string]string{
	"78C43374-5AB7-4E81-B9CF-09E8ACD01133": "0.1.0",
	"536EFA93-783D-436E-92BF-598D1938BF2C": "0.1.1",
	"7ECF027D-4F7D-4876-B050-A31A18CEFA8E": "0.3.0",
	"2EF31E5B-5159-4629-8D9D-5C9A9298FE0A": "0.4.0",
	"CECB8AEC-2F03-4697-8924-3E6BB832A7C1": "0.5.0",
	"B63628E2-7996-4FB1-801D-F2F2BB5985AB": "0.5.1",
	"09120BB5-3A58-4A2B-B6E9-09173A441709": "0.6.0",
}

// The fallback scan applies the manager's own reload bounds: at most this
// many directory entries and unit files, each read by unit.VerifyPath under
// unit.MaxFileBytes.
const (
	legacyScanEntries   = 4096
	legacyScanUnitFiles = 1024
)

// legacyStopRelease names the legacy release that a major upgrade replaces,
// or "" when the upgrade does not replace one. Repair, reinstall, uninstall
// and upgrades from a fixed manager keep the maintenance path unchanged.
// upgrade is WIX_UPGRADE_DETECTED, already validated as a GUID list.
func legacyStopRelease(mode, upgrade string) string {
	if mode != modeUpgrade {
		return ""
	}
	for _, part := range strings.Split(upgrade, ";") {
		code := strings.ToUpper(strings.Trim(strings.TrimSpace(part), "{}"))
		if release, ok := legacyStopProducts[code]; ok {
			return release
		}
	}
	return ""
}

// legacyStopConflict decides before anything is recorded or quiesced. It
// reads only the snapshot already taken and the system unit files under
// unitsDir, so a refusal leaves the manager and its units as they were.
//
// With a snapshot, a shutdown stop launches a helper only for an active
// invocation whose captured definition has ExecStop; an activating unit is
// about to be active. The snapshot does not expose ExecStop, so the current
// unit file stands for the captured definition when both carry the same
// accepted configuration revision. Revisions are per-manager sequence
// numbers assigned on every accepted reload or enablement change, not
// content hashes, so a differing revision says only that the definition may
// have changed since the invocation started:
//
//   - An owned service started under an earlier revision whose current file
//     has no ExecStop is refused as ambiguous; restarting it under the
//     current definition resolves that.
//   - A native proxy whose captured adapter has no ExecStop capability never
//     launches a helper and is not inspected. Other kinds launch no helper.
//   - A unit started, or a file changed, after this snapshot is not covered.
//
// Without a snapshot (ordinary control unavailable or saturated) the upgrade
// keeps its dedicated maintenance path only when no service file under
// unitsDir has, or could have, ExecStop. That cannot see an invocation whose
// file has since been removed or edited to drop ExecStop; such an upgrade
// fails as every legacy upgrade did before this check.
func legacyStopConflict(release string, snap *protocol.SnapshotResult, snapErr error, unitsDir string) error {
	if snapErr != nil || snap == nil {
		if snapErr == nil {
			snapErr = errors.New("no unit state returned")
		}
		return legacyStopScan(release, snapErr, unitsDir)
	}
	var stop, stale, unconfirmed []string
	for _, u := range snap.Units {
		if u.ActiveState != "active" && u.ActiveState != "activating" {
			continue
		}
		kind, err := unit.KindFromName(u.Name)
		if err == nil && kind != unit.KindService {
			continue
		}
		if p := u.NativeProxy; p != nil && !p.SupportsExecStop {
			continue
		}
		if err == nil {
			err = unit.CheckLinkName(u.Name)
		}
		if err != nil {
			unconfirmed = append(unconfirmed, fmt.Sprintf("%s (%s: %v)", u.Name, u.ActiveState, err))
			continue
		}
		has, problem := definitionHasExecStop(filepath.Join(unitsDir, u.Name))
		switch {
		case has:
			stop = append(stop, fmt.Sprintf("%s (%s)", u.Name, u.ActiveState))
		case problem != "":
			unconfirmed = append(unconfirmed, fmt.Sprintf("%s (%s: %s)", u.Name, u.ActiveState, problem))
		case u.InvocationConfigRevision != "" && u.InvocationConfigRevision != u.ConfigRevision:
			stale = append(stale, fmt.Sprintf("%s (%s, started under revision %s, current %q)", u.Name, u.ActiveState, u.InvocationConfigRevision, u.ConfigRevision))
		}
	}
	if len(stop) == 0 && len(stale) == 0 && len(unconfirmed) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "preflight conflict: the installed %s manager cannot run ExecStop in its upgrade shutdown. Keep supervisors paused, use winctl stop <unit> on the units below in dependency order, verify inactive status, then retry. Nothing was changed.", release)
	if len(stop) > 0 {
		fmt.Fprintf(&b, " Active with ExecStop: %s.", strings.Join(stop, ", "))
	}
	if len(stale) > 0 {
		fmt.Fprintf(&b, " Started under an earlier configuration, so the ExecStop they captured is unknown: %s.", strings.Join(stale, ", "))
	}
	if len(unconfirmed) > 0 {
		fmt.Fprintf(&b, " ExecStop could not be confirmed: %s.", strings.Join(unconfirmed, "; "))
	}
	return errors.New(b.String())
}

// legacyStopScan is the fallback when ordinary control gave no snapshot.
func legacyStopScan(release string, snapErr error, unitsDir string) error {
	lead := fmt.Sprintf("preflight conflict: the installed %s manager cannot run ExecStop in its upgrade shutdown, and its ordinary control gave no unit state (%v), so this check cannot tell which system units are running.", release, snapErr)
	restore := " Restore ordinary control and verify winctl snapshot succeeds. Drain affected active units with winctl stop before retrying. Nothing was changed."
	stop, unknown, err := scanExecStopFiles(unitsDir)
	if err != nil {
		return fmt.Errorf("%s Its unit files could not be inspected either (%v).%s Nothing was changed", lead, err, restore)
	}
	if len(stop) == 0 && len(unknown) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(lead)
	b.WriteString(restore)
	if len(stop) > 0 {
		fmt.Fprintf(&b, " Unit files with ExecStop: %s.", strings.Join(stop, ", "))
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&b, " Unit files that could not be classified: %s.", strings.Join(unknown, "; "))
	}
	return errors.New(b.String())
}

// scanExecStopFiles inspects every service file the manager would load from
// unitsDir, within the manager's reload bounds. A missing directory holds no
// definitions, as it does for the manager.
func scanExecStopFiles(unitsDir string) (stop, unknown []string, err error) {
	f, err := os.Open(unitsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	entries, err := f.ReadDir(legacyScanEntries + 1)
	if err == io.EOF {
		err = nil
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, nil, err
	}
	if len(entries) > legacyScanEntries {
		return nil, nil, fmt.Errorf("%s has more than %d entries", unitsDir, legacyScanEntries)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	files := 0
	for _, e := range entries {
		kind, kindErr := unit.KindFromName(e.Name())
		if e.IsDir() || kindErr != nil {
			continue
		}
		if files++; files > legacyScanUnitFiles {
			return nil, nil, fmt.Errorf("%s has more than %d unit files", unitsDir, legacyScanUnitFiles)
		}
		if kind != unit.KindService {
			continue
		}
		has, problem := definitionHasExecStop(filepath.Join(unitsDir, e.Name()))
		switch {
		case has:
			stop = append(stop, e.Name())
		case problem != "":
			unknown = append(unknown, fmt.Sprintf("%s (%s)", e.Name(), problem))
		}
	}
	return stop, unknown, nil
}

// definitionHasExecStop reads and parses one unit file as the manager's
// reload does. A parsed ExecStop counts even when other directives are
// invalid; without one, an unreadable or invalid file returns its
// diagnostics rather than being taken as safe.
func definitionHasExecStop(path string) (bool, string) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err.Error()
	}
	reparse, err := pathIsReparse(path, info)
	if err != nil {
		return false, err.Error()
	}
	if reparse || !info.Mode().IsRegular() {
		return false, "unit definition must be an ordinary file"
	}
	rep := unit.VerifyPath(path)
	if rep.Unit != nil && rep.Unit.Service != nil && len(rep.Unit.Service.ExecStop) > 0 {
		return true, ""
	}
	if rep.HasError() {
		var diags []string
		for _, iss := range rep.Errors() {
			diags = append(diags, iss.String())
		}
		return false, strings.Join(diags, "; ")
	}
	if rep.Unit == nil {
		return false, "unit file produced no definition"
	}
	return false, ""
}
