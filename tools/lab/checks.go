package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

//go:embed assets/runtime-checks.ps1
var runtimeChecks string

//go:embed assets/admission-checks.ps1
var admissionChecks string

//go:embed assets/configuration-checks.ps1
var configurationChecks string

type checkScenario struct {
	name   string
	script string
	passed []string
}

func qualificationScenario(name string) (checkScenario, error) {
	switch name {
	case "configuration":
		return checkScenario{name, configurationChecks, []string{
			"invalid-candidate-atomic", "live-revision-retained", "fresh-start-adopts-revision",
			"removed-live-stop-access", "scm-stop-no-survivors",
		}}, nil
	case "runtime":
		return checkScenario{name, runtimeChecks, []string{
			"oneshot-5000-stdout-5000-stderr-and-unterminated-output",
			"reload-deletion-status-logs-stop-recreation", "ten-notify-ready-stop-reopen-cycles",
		}}, nil
	case "admission":
		return checkScenario{name, admissionChecks, []string{
			"32-pending-starts", "overload-rejected-before-launch", "stop-while-full",
			"released-slot-reused", "accepted-completions-drained", "scm-stop-no-survivors",
		}}, nil
	default:
		return checkScenario{}, fmt.Errorf("unknown qualification scenario")
	}
}

func checkResult(raw []byte, commit string, scenario checkScenario) error {
	var result struct {
		Schema    int      `json:"schema"`
		Commit    string   `json:"commit"`
		Fixture   string   `json:"fixture_sha256"`
		Build     string   `json:"build"`
		Identity  string   `json:"identity"`
		Passed    []string `json:"passed"`
		Completed string   `json:"completed"`
	}
	if len(raw) > 16<<10 || json.Unmarshal(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}), &result) != nil {
		return fmt.Errorf("invalid qualification result")
	}
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte(scenario.script)))
	if result.Schema != 1 || result.Commit != commit || result.Fixture != expectedHash || result.Identity != "SYSTEM" || !regexp.MustCompile(`^[0-9]+\.[0-9]+$`).MatchString(result.Build) {
		return fmt.Errorf("qualification identity mismatch")
	}
	if _, err := time.Parse(time.RFC3339Nano, result.Completed); err != nil {
		return fmt.Errorf("invalid qualification completion time")
	}
	if len(result.Passed) != len(scenario.passed) {
		return fmt.Errorf("incomplete qualification assertions")
	}
	seen := make(map[string]bool)
	for _, item := range result.Passed {
		if seen[item] {
			return fmt.Errorf("duplicate qualification assertion")
		}
		seen[item] = true
	}
	for _, item := range scenario.passed {
		if !seen[item] {
			return fmt.Errorf("missing qualification assertion")
		}
	}
	return nil
}

func checkGuest(a *api, c config, vmid int, dir, commit, name string) error {
	scenario, err := qualificationScenario(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "maintenance.lock")); !os.IsNotExist(err) {
		return fmt.Errorf("maintenance gate is active or unreadable")
	}
	archive, err := smokeArchive(dir, commit)
	if err != nil {
		return err
	}
	// Use the already validated archive's exact manifest bytes, not a second
	// directory read that could race replacement of the local artifact set.
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return err
	}
	f, err := z.Open("build-manifest.json")
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(f)
	if err := errors.Join(readErr, f.Close()); err != nil {
		return err
	}
	manifestHash := sha256.Sum256(raw)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	r, _, err := ownedGuest(ctx, a, c, vmid)
	if err != nil {
		return err
	}
	if r.State != "smoke-passed" || r.Commit != commit {
		return fmt.Errorf("matching completed service smoke required before checks")
	}
	prefix := filepath.Join(c.StateDir, r.ID+"-"+name+"-checks")
	preflight := fmt.Sprintf(`$ErrorActionPreference='Stop'
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'Expected SYSTEM' }
if (Get-NetRoute -DestinationPrefix @('0.0.0.0/0','::/0') -ErrorAction SilentlyContinue) { throw 'Disconnect maintenance routing' }
if ((Get-FileHash 'C:\winunitd-lab\build-manifest.json').Hash -ne '%x') { throw 'Installed manifest differs from admitted artifacts' }
$service=Get-CimInstance Win32_Service -Filter "Name='winunitd'"
if (!$service -or $service.State -ne 'Stopped' -or $service.PathName -notmatch '^"?C:\\winunitd-lab\\winunitd\.exe"?\s') { throw 'Expected stopped isolated lab service' }
foreach ($suffix in @('.ps1','.log','-result.json')) { if (Test-Path -LiteralPath ('C:\winunitd-lab\%s-checks'+$suffix)) { throw 'Fresh scenario namespace required' } }
`, manifestHash, name)
	if _, err := guestExec(ctx, a, c, vmid, preflight); err != nil {
		return err
	}
	args := map[string]any{"file": `C:\winunitd-lab\` + name + "-checks.ps1", "content": base64.StdEncoding.EncodeToString([]byte(scenario.script)), "encode": false}
	if err := a.callJSON(ctx, http.MethodPost, fmt.Sprintf("/nodes/%s/qemu/%d/agent/file-write", c.Node, vmid), args, nil); err != nil {
		return err
	}
	fmt.Printf("Running %s qualification against admitted artifacts...\n", name)
	out, runErr := guestExec(ctx, a, c, vmid, fmt.Sprintf(`$ErrorActionPreference='Stop'; & C:\winunitd-lab\%s-checks.ps1 -DisposableLab`, name))
	if err := os.WriteFile(prefix+"-console.log", []byte(out), 0600); err != nil {
		return errors.Join(runErr, err)
	}
	// Collect private evidence even when the scenario reports a failure.
	export := fmt.Sprintf(`$ErrorActionPreference='Stop'
$out=@{}
foreach ($suffix in @('.log','-result.json')) {
 $path='C:\winunitd-lab\%s-checks'+$suffix
 if (Test-Path -LiteralPath $path) {
  if ((Get-Item -LiteralPath $path).Length -gt 1048576) { throw 'Scenario evidence exceeds export budget' }
  $out[$suffix]=[IO.File]::ReadAllText($path)
 }
}
$out | ConvertTo-Json -Compress
`, name)
	evidenceCtx, evidenceCancel := context.WithTimeout(context.Background(), time.Minute)
	defer evidenceCancel()
	output, exportErr := guestExec(evidenceCtx, a, c, vmid, export)
	if exportErr != nil {
		return errors.Join(runErr, exportErr)
	}
	var evidence map[string]string
	if json.Unmarshal([]byte(output), &evidence) != nil {
		return errors.Join(runErr, fmt.Errorf("invalid scenario evidence envelope"))
	}
	for _, suffix := range []string{".log", "-result.json"} {
		if value, ok := evidence[suffix]; ok {
			if err := os.WriteFile(prefix+suffix, []byte(value), 0600); err != nil {
				return errors.Join(runErr, err)
			}
		}
	}
	if runErr != nil {
		return runErr
	}
	if err := checkResult([]byte(evidence["-result.json"]), commit, scenario); err != nil {
		return err
	}
	fmt.Printf("%s qualification passed; identities, assertions, and private evidence verified.\n", name)
	return nil
}
