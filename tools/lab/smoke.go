package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

//go:embed assets/smoke-before.ps1
var smokeBefore string

//go:embed assets/smoke-after.ps1
var smokeAfter string

type smokeManifest struct {
	Schema    int    `json:"schema"`
	Commit    string `json:"commit"`
	Dirty     bool   `json:"dirty"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	Artifacts []struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
		Size   int    `json:"size"`
	} `json:"artifacts"`
}

func smokeArchive(dir, commit string) ([]byte, error) {
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) {
		return nil, fmt.Errorf("full admitted source commit required")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "build-manifest.json"))
	if err != nil {
		return nil, err
	}
	var m smokeManifest
	if json.Unmarshal(raw, &m) != nil || m.Schema != 1 || m.Commit != commit || m.Dirty || m.GOOS != "windows" || m.GOARCH != "amd64" {
		return nil, fmt.Errorf("artifact identity does not match clean admitted Windows build")
	}
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	add := func(name string, data []byte) error {
		f, err := w.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	seen := map[string]bool{}
	for _, artifact := range m.Artifacts {
		if seen[artifact.Name] || (artifact.Name != "winunitd.exe" && artifact.Name != "winctl.exe" && artifact.Name != "winunit-notify.exe") {
			return nil, fmt.Errorf("unexpected or duplicate artifact")
		}
		seen[artifact.Name] = true
		if artifact.Size <= 0 || artifact.Size > 64<<20 {
			return nil, fmt.Errorf("artifact size outside admission bounds")
		}
		data, err := os.ReadFile(filepath.Join(dir, artifact.Name))
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(data)
		if len(data) != artifact.Size || hex.EncodeToString(hash[:]) != artifact.SHA256 {
			return nil, fmt.Errorf("artifact hash/size mismatch")
		}
		if err := add(artifact.Name, data); err != nil {
			return nil, err
		}
	}
	if len(seen) != 3 {
		return nil, fmt.Errorf("incomplete artifact set")
	}
	for _, entry := range []struct {
		name string
		data []byte
	}{{"build-manifest.json", raw}, {"smoke-before.ps1", []byte(smokeBefore)}, {"smoke-after.ps1", []byte(smokeAfter)}} {
		if err := add(entry.name, entry.data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func smokeGuest(a *api, c config, vmid int, dir, commit string) error {
	archive, err := smokeArchive(dir, commit)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	r, path, err := ownedGuest(ctx, a, c, vmid)
	if err != nil {
		return err
	}
	if r.State != "ready" {
		return fmt.Errorf("fresh ready guest required for smoke")
	}
	r.State = "smoke-transferring"
	hash := sha256.Sum256(archive)
	r.Commit, r.ArchiveHash = commit, fmt.Sprintf("%x", hash)
	if err := saveRecord(path, r); err != nil {
		return err
	}
	if _, err := guestExec(ctx, a, c, vmid, `$ErrorActionPreference='Stop'; if (Test-Path C:\winunitd-lab) { throw 'Fresh guest required' }; New-Item -ItemType Directory C:\winunitd-lab | Out-Null; & icacls.exe C:\winunitd-lab /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null; if ($LASTEXITCODE) { throw 'ACL failed' }`); err != nil {
		return err
	}
	// Bound individual API requests; parts stay in the protected disposable guest.
	parts := 0
	for offset := 0; offset < len(archive); offset += 32 << 10 {
		end := min(offset+(32<<10), len(archive))
		args := map[string]any{"file": fmt.Sprintf(`C:\winunitd-lab\part-%04d`, parts), "content": base64.StdEncoding.EncodeToString(archive[offset:end]), "encode": false}
		if err := a.callJSON(ctx, http.MethodPost, fmt.Sprintf("/nodes/%s/qemu/%d/agent/file-write", c.Node, vmid), args, nil); err != nil {
			return err
		}
		parts++
	}
	assemble := fmt.Sprintf(`$ErrorActionPreference='Stop'
$base='C:\winunitd-lab'
$stream=[IO.File]::Create("$base\payload.zip")
try { for ($i=0; $i -lt %d; $i++) { $bytes=[IO.File]::ReadAllBytes(('{0}\part-{1:D4}' -f $base,$i)); $stream.Write($bytes,0,$bytes.Length) } } finally { $stream.Dispose() }
if ((Get-FileHash "$base\payload.zip").Hash -ne '%x') { throw 'Archive hash mismatch' }
Expand-Archive "$base\payload.zip" $base
$m=Get-Content "$base\build-manifest.json" -Raw | ConvertFrom-Json
foreach ($a in $m.artifacts) { if ((Get-FileHash (Join-Path $base $a.name)).Hash -ne $a.sha256) { throw 'Guest artifact hash mismatch' } }
& "$base\smoke-before.ps1"
`, parts, hash)
	out, err := guestExec(ctx, a, c, vmid, assemble)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(c.StateDir, r.ID+"-smoke-before.log"), []byte(out), 0600); err != nil {
		return err
	}
	r.State = "smoke-rebooting"
	if err := saveRecord(path, r); err != nil {
		return err
	}
	fmt.Println("Artifact hashes and pre-reboot service smoke passed; rebooting guest.")
	var task string
	if err := a.call(ctx, http.MethodPost, fmt.Sprintf("/nodes/%s/qemu/%d/status/reboot", c.Node, vmid), nil, &task); err != nil {
		return err
	}
	if err := a.waitTask(ctx, c.Node, task); err != nil {
		return err
	}
	// The readiness probe is read-only and safe to retry while QGA starts.
	readyCtx, readyCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer readyCancel()
	for {
		_, err = guestExec(readyCtx, a, c, vmid, "Write-Output ready")
		if err == nil {
			break
		}
		select {
		case <-readyCtx.Done():
			return readyCtx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	out, err = guestExec(ctx, a, c, vmid, `$ErrorActionPreference='Stop'; & C:\winunitd-lab\smoke-after.ps1`)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(c.StateDir, r.ID+"-smoke-after.log"), []byte(out), 0600); err != nil {
		return err
	}
	result := map[string]any{"schema": 1, "scenario": "system-reboot-smoke", "commit": commit, "archive_sha256": fmt.Sprintf("%x", hash), "passed": true, "completed": time.Now().UTC()}
	data, _ := json.MarshalIndent(result, "", "  ")
	if err := os.WriteFile(filepath.Join(c.StateDir, r.ID+"-smoke-result.json"), data, 0600); err != nil {
		return err
	}
	r.State = "smoke-passed"
	if err := saveRecord(path, r); err != nil {
		return err
	}
	fmt.Println("SYSTEM service smoke, reboot recovery, and SCM stop passed; private evidence saved.")
	return nil
}
