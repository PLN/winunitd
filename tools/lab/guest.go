package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

func ownedGuest(ctx context.Context, a *api, c config, vmid int) (runRecord, string, error) {
	var zero runRecord
	if vmid < c.FirstVMID || vmid > c.LastVMID || c.FirstVMID < 100 {
		return zero, "", fmt.Errorf("guest outside controller range")
	}
	var pool struct {
		Members []struct {
			VMID int    `json:"vmid"`
			Node string `json:"node"`
		} `json:"members"`
	}
	if err := a.call(ctx, http.MethodGet, "/pools/"+c.Pool, nil, &pool); err != nil {
		return zero, "", err
	}
	found := false
	for _, member := range pool.Members {
		if member.VMID == vmid && member.Node == c.Node {
			found = true
		}
	}
	if !found {
		return zero, "", fmt.Errorf("guest not in configured pool/node")
	}
	var vm map[string]any
	if err := a.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/qemu/%d/config", c.Node, vmid), nil, &vm); err != nil {
		return zero, "", err
	}
	net0, _ := vm["net0"].(string)
	description, _ := vm["description"].(string)
	for key := range vm {
		if key != "net0" && regexp.MustCompile(`^net[0-9]+$`).MatchString(key) {
			return zero, "", fmt.Errorf("guest has an unexpected network interface")
		}
	}
	if !strings.Contains(","+net0+",", ",bridge="+c.Bridge+",") {
		return zero, "", fmt.Errorf("guest network ownership drift")
	}
	files, err := filepath.Glob(filepath.Join(c.StateDir, "*.json"))
	if err != nil {
		return zero, "", err
	}
	for _, path := range files {
		if !regexp.MustCompile(`^[0-9a-f]{32}\.json$`).MatchString(filepath.Base(path)) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return zero, "", err
		}
		var r runRecord
		if err := json.Unmarshal(data, &r); err != nil {
			return zero, "", fmt.Errorf("invalid private run record")
		}
		if r.Schema == 1 && r.VMID == vmid && r.Node == c.Node && r.Pool == c.Pool && r.Marker == description && r.Marker == "winunitd-lab-run:"+r.ID && len(r.ID) == 32 {
			return r, path, nil
		}
	}
	return zero, "", fmt.Errorf("no matching controller ownership record")
}

func powershellEncoded(script string) string {
	units := utf16.Encode([]rune(script))
	data := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(data[2*i:], u)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func detachMedia(a *api, c config, vmid int) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, path, err := ownedGuest(ctx, a, c, vmid)
	if err != nil {
		return err
	}
	if r.State != "ready" && r.State != "smoke-passed" {
		return fmt.Errorf("media removal requires completed setup/scenario")
	}
	endpoint := fmt.Sprintf("/nodes/%s/qemu/%d/config", c.Node, vmid)
	var vm map[string]any
	if err := a.call(ctx, http.MethodGet, endpoint, nil, &vm); err != nil {
		return err
	}
	var remove []string
	for device, expected := range map[string]string{"sata1": r.OSISO, "sata2": r.BootstrapISO} {
		value, exists := vm[device]
		if !exists {
			continue
		}
		text, ok := value.(string)
		if !ok || strings.Split(text, ",")[0] != expected || !strings.Contains(","+text+",", ",media=cdrom,") {
			return fmt.Errorf("installation media ownership drift")
		}
		remove = append(remove, device)
	}
	if len(remove) != 0 {
		args := map[string]any{"delete": strings.Join(remove, ","), "boot": "order=sata0", "digest": vm["digest"]}
		if err := a.callJSON(ctx, http.MethodPut, endpoint, args, nil); err != nil {
			return err
		}
	}
	var current map[string]any
	if err := a.call(ctx, http.MethodGet, endpoint, url.Values{"current": {"1"}}, &current); err != nil {
		return err
	}
	for _, device := range []string{"sata1", "sata2"} {
		if _, exists := current[device]; exists {
			r.MediaDetached = false
			if err := saveRecord(path, r); err != nil {
				return err
			}
			return fmt.Errorf("media removal is pending; power-cycle the guest and retry before deleting its ISO")
		}
	}
	r.MediaDetached = true
	if err := saveRecord(path, r); err != nil {
		return err
	}
	fmt.Println("Owned installation media detached; remove private credential-bearing ISO separately.")
	return nil
}

func guestExec(ctx context.Context, a *api, c config, vmid int, script string) (string, error) {
	endpoint := fmt.Sprintf("/nodes/%s/qemu/%d/agent/", c.Node, vmid)
	var started struct {
		PID int `json:"pid"`
	}
	if err := a.callJSON(ctx, http.MethodPost, endpoint+"exec", map[string]any{"command": []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", powershellEncoded(script)}}, &started); err != nil {
		return "", err
	}
	if started.PID <= 0 {
		return "", fmt.Errorf("guest command did not return a process identity")
	}
	for {
		var status struct {
			Exited         int             `json:"exited"`
			ExitCode       int             `json:"exitcode"`
			Signal         int             `json:"signal,omitempty"`
			Output         string          `json:"out-data"`
			Error          string          `json:"err-data"`
			Truncated      json.RawMessage `json:"out-truncated,omitempty"`
			ErrorTruncated json.RawMessage `json:"err-truncated,omitempty"`
		}
		if err := a.call(ctx, http.MethodGet, endpoint+"exec-status", url.Values{"pid": {strconv.Itoa(started.PID)}}, &status); err != nil {
			return "", err
		}
		if status.Exited != 0 {
			if status.ExitCode != 0 || status.Signal != 0 || outputTruncated(status.Truncated) || outputTruncated(status.ErrorTruncated) {
				if err := saveGuestFailure(c, vmid, started.PID, script, status); err != nil {
					return "", fmt.Errorf("guest command failed; private diagnostics could not be saved")
				}
				return "", fmt.Errorf("guest command failed or output was truncated; diagnostics saved in private controller state")
			}
			return status.Output, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Proxmox versions may encode flags as JSON booleans or integers.
// Unknown representations fail closed rather than accepting incomplete evidence.
func outputTruncated(raw json.RawMessage) bool {
	return len(raw) != 0 && string(raw) != "false" && string(raw) != "0" && string(raw) != "null"
}

func saveGuestFailure(c config, vmid, pid int, script string, status any) error {
	if !filepath.IsAbs(c.StateDir) {
		return fmt.Errorf("private state directory required")
	}
	data, err := json.MarshalIndent(map[string]any{
		"schema": 1, "vmid": vmid, "pid": pid, "captured": time.Now().UTC(),
		"command_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(script))), "status": status,
	}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(c.StateDir, "guest-failure-*.json")
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	return errors.Join(writeErr, f.Close())
}

func waitGuest(a *api, c config, vmid int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	r, path, err := ownedGuest(ctx, a, c, vmid)
	if err != nil {
		return err
	}
	if r.State != "booting" && r.State != "ready" {
		return fmt.Errorf("readiness wait cannot overwrite an active or completed scenario")
	}
	script := `$ErrorActionPreference = 'Stop'
if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne 'S-1-5-18') { throw 'SYSTEM identity required' }
$ready = Join-Path $env:ProgramData 'winunitd-lab-bootstrap\ready.json'
if (Test-Path $ready) { Get-Content $ready -Raw } else { '{"ready":false}' }
`
	lastProgress := time.Time{}
	for {
		attempt, stop := context.WithTimeout(ctx, 20*time.Second)
		out, err := guestExec(attempt, a, c, vmid, script)
		stop()
		if err == nil {
			var state struct {
				Ready bool `json:"ready"`
			}
			if err := json.Unmarshal([]byte(out), &state); err != nil {
				return fmt.Errorf("invalid bootstrap readiness response")
			}
			if state.Ready {
				if err := os.WriteFile(filepath.Join(c.StateDir, r.ID+"-platform.json"), []byte(out), 0600); err != nil {
					return err
				}
				r.State = "ready"
				if err := saveRecord(path, r); err != nil {
					return err
				}
				fmt.Println("Fresh Windows setup and SYSTEM guest-agent handshake passed.")
				return nil
			}
		} else {
			var status apiStatusError
			if errors.As(err, &status) && (status.Code == 401 || status.Code == 403) {
				return err
			}
		}
		if time.Since(lastProgress) >= 30*time.Second {
			fmt.Println("Waiting for fresh Windows setup and guest-agent readiness...")
			lastProgress = time.Now()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}
