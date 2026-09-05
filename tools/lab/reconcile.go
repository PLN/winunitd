package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// Reconciliation is an inventory audit. It never guesses which orphan to delete
// or rewrites a run record that another operation may still be using.
func reconcile(a *api, c config) error {
	if !filepath.IsAbs(c.StateDir) || c.FirstVMID < 100 || c.LastVMID < c.FirstVMID || c.LastVMID > 65535 || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(c.Storage) {
		return fmt.Errorf("private state directory, storage, and valid guest range required")
	}
	if info, err := os.Stat(c.StateDir); err != nil || !info.IsDir() {
		return fmt.Errorf("private state directory must already exist")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	members := map[int]string{}
	for _, member := range pool.Members {
		if member.Type == "qemu" && member.VMID >= c.FirstVMID && member.VMID <= c.LastVMID {
			members[member.VMID] = member.Node
		}
	}
	files, err := filepath.Glob(filepath.Join(c.StateDir, "*.json"))
	if err != nil {
		return err
	}
	owned, replaced, absent, orphans, expired := 0, 0, 0, 0, 0
	matched := map[int]bool{}
	for _, path := range files {
		if !regexp.MustCompile(`^[0-9a-f]{32}\.json$`).MatchString(filepath.Base(path)) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var r runRecord
		if json.Unmarshal(data, &r) != nil || r.Schema != 1 || r.ID+".json" != filepath.Base(path) || r.Marker != "winunitd-lab-run:"+r.ID {
			return fmt.Errorf("invalid private run record")
		}
		if r.Node != c.Node || r.Pool != c.Pool || r.VMID < c.FirstVMID || r.VMID > c.LastVMID {
			continue
		}
		if node, exists := members[r.VMID]; exists {
			if node != c.Node {
				replaced++
				continue
			}
			var vm struct {
				Description string `json:"description"`
			}
			if err := a.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/qemu/%d/config", c.Node, r.VMID), nil, &vm); err != nil {
				return err
			}
			if vm.Description != r.Marker {
				replaced++
				continue
			}
			owned++
			matched[r.VMID] = true
			deadline := r.Expires
			if deadline.IsZero() {
				deadline = r.Created.Add(24 * time.Hour)
			}
			if !deadline.After(time.Now().UTC()) {
				expired++
			}
		} else {
			absent++
			var volumes []json.RawMessage
			if err := a.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/storage/%s/content", c.Node, c.Storage), url.Values{"vmid": {strconv.Itoa(r.VMID)}, "content": {"images"}}, &volumes); err != nil {
				return err
			}
			if len(volumes) != 0 {
				orphans++
			}
		}
	}
	unmanaged := len(members) - len(matched)
	fmt.Printf("Reconciliation: %d owned, %d replaced identities, %d absent, %d orphaned storage sets, %d expired, %d unmanaged.\n", owned, replaced, absent, orphans, expired, unmanaged)
	if orphans != 0 || expired != 0 || unmanaged != 0 {
		return fmt.Errorf("private inventory needs operator reconciliation; no resources changed")
	}
	return nil
}
