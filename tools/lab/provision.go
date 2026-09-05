package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type runRecord struct {
	Schema        int       `json:"schema"`
	ID            string    `json:"id"`
	Node          string    `json:"node"`
	Pool          string    `json:"pool"`
	VMID          int       `json:"vmid"`
	Marker        string    `json:"marker"`
	Created       time.Time `json:"created"`
	State         string    `json:"state"`
	OSISO         string    `json:"os_iso"`
	BootstrapISO  string    `json:"bootstrap_iso"`
	Commit        string    `json:"commit,omitempty"`
	ArchiveHash   string    `json:"archive_sha256,omitempty"`
	MediaDetached bool      `json:"media_detached,omitempty"`
}

func createGuest(a *api, c config, vmid int, osISO, bootstrapISO string) error {
	name := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
	if !name.MatchString(c.Storage) || !name.MatchString(c.Bridge) || c.FirstVMID < 100 || c.LastVMID > 65535 || vmid < c.FirstVMID || vmid > c.LastVMID {
		return fmt.Errorf("invalid or out-of-range provisioning configuration")
	}
	volume := regexp.MustCompile(`^[a-zA-Z0-9_-]+:iso/[a-zA-Z0-9_.-]+\.iso$`)
	for _, iso := range []string{osISO, bootstrapISO} {
		if !volume.MatchString(iso) || !strings.HasPrefix(iso, c.Storage+":iso/") {
			return fmt.Errorf("installation media must be in dedicated lab storage")
		}
	}
	mac, err := net.ParseMAC(c.MACPrefix + ":00:00:00")
	if err != nil || len(mac) != 6 || mac[0]&1 != 0 {
		return fmt.Errorf("invalid private MAC prefix")
	}
	mac[4], mac[5] = byte(vmid>>8), byte(vmid)
	if !filepath.IsAbs(c.StateDir) {
		return fmt.Errorf("private absolute state directory required")
	}
	if err := os.MkdirAll(c.StateDir, 0700); err != nil {
		return err
	}
	// Serialize admission. A stale lock is retained for operator reconciliation.
	lockPath := filepath.Join(c.StateDir, "admission.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("controller admission locked; inspect its private state")
	}
	defer os.Remove(lockPath)
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var pool struct {
		Members []struct {
			VMID int    `json:"vmid"`
			Node string `json:"node"`
			Type string `json:"type"`
		} `json:"members"`
	}
	if err := a.call(ctx, http.MethodGet, "/pools/"+c.Pool, nil, &pool); err != nil {
		return err
	}
	running := 0
	for _, member := range pool.Members {
		if member.Type != "qemu" {
			continue
		}
		var status struct {
			Status string `json:"status"`
		}
		if err := a.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/qemu/%d/status/current", member.Node, member.VMID), nil, &status); err != nil {
			return err
		}
		if status.Status == "running" {
			running++
		}
	}
	if running >= 2 {
		return fmt.Errorf("two-guest concurrency limit reached")
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return err
	}
	id := hex.EncodeToString(idBytes)
	r := runRecord{Schema: 1, ID: id, Node: c.Node, Pool: c.Pool, VMID: vmid, Marker: "winunitd-lab-run:" + id, Created: time.Now().UTC(), State: "allocating", OSISO: osISO, BootstrapISO: bootstrapISO}
	recordPath := filepath.Join(c.StateDir, id+".json")
	if err := saveRecord(recordPath, r); err != nil {
		return err
	}
	args := map[string]any{
		"vmid": vmid, "pool": c.Pool, "name": fmt.Sprintf("lab-%d", vmid), "description": r.Marker,
		"cores": 4, "memory": 8192, "cpu": "x86-64-v3", "machine": "q35", "ostype": "win11", "bios": "ovmf", "onboot": false, "localtime": false, "agent": "enabled=1",
		"efidisk0": c.Storage + ":0,efitype=4m,pre-enrolled-keys=1", "tpmstate0": c.Storage + ":0,version=v2.0",
		"sata0": c.Storage + ":80", "sata1": osISO + ",media=cdrom", "sata2": bootstrapISO + ",media=cdrom",
		"boot": "order=sata0;sata1", "net0": "e1000=" + mac.String() + ",bridge=" + c.Bridge + ",firewall=1",
	}
	var task string
	if err := a.callJSON(ctx, http.MethodPost, "/nodes/"+c.Node+"/qemu", args, &task); err != nil {
		return err
	}
	if err := a.waitTask(ctx, c.Node, task); err != nil {
		return err
	}
	// Pool membership now exists, so tag permission checks use the scoped ACL.
	if err := a.callJSON(ctx, http.MethodPut, fmt.Sprintf("/nodes/%s/qemu/%d/config", c.Node, vmid), map[string]string{"tags": "winunitd-lab"}, nil); err != nil {
		return err
	}
	r.State = "allocated"
	if err := saveRecord(recordPath, r); err != nil {
		return err
	}
	if err := a.call(ctx, http.MethodPost, fmt.Sprintf("/nodes/%s/qemu/%d/status/start", c.Node, vmid), nil, &task); err != nil {
		return err
	}
	if err := a.waitTask(ctx, c.Node, task); err != nil {
		return err
	}
	r.State = "booting"
	if err := saveRecord(recordPath, r); err != nil {
		return err
	}
	// Official installation media prompts for a key on a new empty disk.
	// Bound this to the initial boot; never send keys to qualification workloads.
	for i := 0; i < 15; i++ {
		if err := a.callJSON(ctx, http.MethodPut, fmt.Sprintf("/nodes/%s/qemu/%d/sendkey", c.Node, vmid), map[string]string{"key": "ret"}, nil); err != nil {
			return err
		}
		time.Sleep(time.Second)
	}
	fmt.Println("Owned guest allocated and started; private run record saved.")
	return nil
}

func saveRecord(path string, r runRecord) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".record-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Windows scanners can briefly hold a destination without delete sharing.
	// Retry replacement, never remove the previous durable record first.
	for attempt := 0; ; attempt++ {
		err = os.Rename(f.Name(), path)
		if err == nil || runtime.GOOS != "windows" || !os.IsPermission(err) || attempt == 5 {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
