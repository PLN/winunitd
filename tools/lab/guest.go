package main

import (
	"context"
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
	var vm struct {
		Description string `json:"description"`
		Net0        string `json:"net0"`
	}
	if err := a.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/qemu/%d/config", c.Node, vmid), nil, &vm); err != nil {
		return zero, "", err
	}
	if !strings.Contains(","+vm.Net0+",", ",bridge="+c.Bridge+",") {
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
		if r.Schema == 1 && r.VMID == vmid && r.Node == c.Node && r.Pool == c.Pool && r.Marker == vm.Description && r.Marker == "winunitd-lab-run:"+r.ID && len(r.ID) == 32 {
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
			Exited    int    `json:"exited"`
			ExitCode  int    `json:"exitcode"`
			Output    string `json:"out-data"`
			Truncated int    `json:"out-truncated"`
		}
		if err := a.call(ctx, http.MethodGet, endpoint+"exec-status", url.Values{"pid": {strconv.Itoa(started.PID)}}, &status); err != nil {
			return "", err
		}
		if status.Exited != 0 {
			if status.ExitCode != 0 || status.Truncated != 0 {
				return "", fmt.Errorf("guest command failed or output was truncated")
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
