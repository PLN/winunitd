package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func retirementDevices(vm map[string]any, c config, vmid int) error {
	device := regexp.MustCompile(`^(ide|sata|scsi|virtio|efidisk|tpmstate|unused|hostpci|usb|net)[0-9]+$`)
	volume := regexp.MustCompile(`^` + regexp.QuoteMeta(c.Storage) + `:` + strconv.Itoa(vmid) + `/vm-` + strconv.Itoa(vmid) + `-disk-[0-9]+\.(raw|qcow2)$`)
	for key, value := range vm {
		if key == "args" || key == "hookscript" {
			return fmt.Errorf("guest has unmanaged extensions")
		}
		if !device.MatchString(key) {
			continue
		}
		if key == "net0" {
			continue
		}
		if key != "sata0" && key != "efidisk0" && key != "tpmstate0" {
			return fmt.Errorf("guest has unexpected devices; inspect before retirement")
		}
		text, ok := value.(string)
		if !ok || !volume.MatchString(strings.Split(text, ",")[0]) {
			return fmt.Errorf("guest disk ownership drift")
		}
	}
	return nil
}

func retireGuest(a *api, c config, vmid int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r, path, err := ownedGuest(ctx, a, c, vmid)
	if err != nil {
		return err
	}
	if (r.State != "ready" && r.State != "smoke-passed") || !r.MediaDetached {
		return fmt.Errorf("retirement requires completed setup/scenario and detached media")
	}
	endpoint := fmt.Sprintf("/nodes/%s/qemu/%d", c.Node, vmid)
	check := func() error {
		if _, _, err := ownedGuest(ctx, a, c, vmid); err != nil {
			return err
		}
		for _, query := range []url.Values{nil, {"current": {"1"}}} {
			var vm map[string]any
			if err := a.call(ctx, http.MethodGet, endpoint+"/config", query, &vm); err != nil {
				return err
			}
			if err := retirementDevices(vm, c, vmid); err != nil {
				return err
			}
		}
		var snapshots []struct {
			Name string `json:"name"`
		}
		if err := a.call(ctx, http.MethodGet, endpoint+"/snapshot", nil, &snapshots); err != nil {
			return err
		}
		for _, snapshot := range snapshots {
			if snapshot.Name != "current" {
				return fmt.Errorf("guest has retained snapshots; inspect before retirement")
			}
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	var status struct {
		Status string `json:"status"`
	}
	if err := a.call(ctx, http.MethodGet, endpoint+"/status/current", nil, &status); err != nil {
		return err
	}
	var task string
	if status.Status != "stopped" {
		if err := a.call(ctx, http.MethodPost, endpoint+"/status/shutdown", url.Values{"timeout": {"120"}, "forceStop": {"0"}}, &task); err != nil {
			return err
		}
		if err := a.waitTask(ctx, c.Node, task); err != nil {
			return err
		}
		if err := a.call(ctx, http.MethodGet, endpoint+"/status/current", nil, &status); err != nil {
			return err
		}
		if status.Status != "stopped" {
			return fmt.Errorf("guest did not stop; no forced retirement performed")
		}
	}
	if err := check(); err != nil {
		return err
	}
	r.State = "retiring"
	if err := saveRecord(path, r); err != nil {
		return err
	}
	if err := a.call(ctx, http.MethodDelete, endpoint, nil, &task); err != nil {
		return err
	}
	if err := a.waitTask(ctx, c.Node, task); err != nil {
		return err
	}
	var pool struct {
		Members []struct {
			VMID int `json:"vmid"`
		} `json:"members"`
	}
	if err := a.call(ctx, http.MethodGet, "/pools/"+c.Pool, nil, &pool); err != nil {
		return err
	}
	for _, member := range pool.Members {
		if member.VMID == vmid {
			return fmt.Errorf("retired guest remains in pool")
		}
	}
	var volumes []struct {
		Volume string `json:"volid"`
	}
	if err := a.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/storage/%s/content", c.Node, c.Storage), url.Values{"vmid": {strconv.Itoa(vmid)}, "content": {"images"}}, &volumes); err != nil {
		return err
	}
	if len(volumes) != 0 {
		return fmt.Errorf("retirement left storage volumes; inspect private inventory")
	}
	r.State = "retired"
	if err := saveRecord(path, r); err != nil {
		return err
	}
	fmt.Println("Owned guest retired; pool membership and guest disk removal verified.")
	return nil
}
