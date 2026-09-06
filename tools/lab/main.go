// Command lab controls only an explicitly configured private qualification pool.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type config struct {
	APIOrigin string `json:"api_origin"`
	CAFile    string `json:"ca_file"`
	TokenFile string `json:"token_file"`
	Node      string `json:"node"`
	Pool      string `json:"pool"`
	DenyVMID  int    `json:"deny_vm_id"`
	Storage   string `json:"storage"`
	Bridge    string `json:"bridge"`
	MACPrefix string `json:"mac_prefix"`
	FirstVMID int    `json:"first_vm_id"`
	LastVMID  int    `json:"last_vm_id"`
	StateDir  string `json:"state_dir"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lab:", err)
		os.Exit(1)
	}
}

func run() error {
	file := flag.String("config", "", "private controller configuration")
	vmid := flag.Int("vmid", 0, "guest ID within the configured range")
	osISO := flag.String("os-iso", "", "allowlisted installation ISO volume")
	bootstrapISO := flag.String("bootstrap-iso", "", "private bootstrap ISO volume")
	artifacts := flag.String("artifacts", "", "directory containing admitted build manifest and binaries")
	commit := flag.String("commit", "", "full reviewed source commit for artifact admission")
	scenario := flag.String("scenario", "", "post-smoke checks: runtime, admission, configuration, or operations")
	flag.Parse()
	if *file == "" || flag.NArg() != 1 {
		return fmt.Errorf("usage: lab -config PRIVATE_FILE [options] probe|create|wait|smoke|checks|detach|retire|reconcile")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	var c config
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return fmt.Errorf("invalid controller configuration: %w", err)
	}
	name := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
	if !name.MatchString(c.Node) || !name.MatchString(c.Pool) {
		return fmt.Errorf("invalid node or pool identifier")
	}
	a, err := newAPI(c.APIOrigin, c.CAFile, c.TokenFile)
	if err != nil {
		return err
	}
	if flag.Arg(0) == "reconcile" {
		return reconcile(a, c)
	}
	if flag.Arg(0) != "probe" {
		if !filepath.IsAbs(c.StateDir) || *vmid < c.FirstVMID || *vmid > c.LastVMID || c.FirstVMID < 100 {
			return fmt.Errorf("private state directory and valid guest range required")
		}
		if err := os.MkdirAll(c.StateDir, 0700); err != nil {
			return err
		}
		lockPath := filepath.Join(c.StateDir, fmt.Sprintf("vm-%d.lock", *vmid))
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("guest operation locked; inspect private controller state")
		}
		defer os.Remove(lockPath)
		defer lock.Close()
	}
	if flag.Arg(0) == "create" {
		return createGuest(a, c, *vmid, *osISO, *bootstrapISO)
	}
	if flag.Arg(0) == "wait" {
		return waitGuest(a, c, *vmid)
	}
	if flag.Arg(0) == "smoke" {
		return smokeGuest(a, c, *vmid, *artifacts, *commit)
	}
	if flag.Arg(0) == "checks" {
		return checkGuest(a, c, *vmid, *artifacts, *commit, *scenario)
	}
	if flag.Arg(0) == "detach" {
		return detachMedia(a, c, *vmid)
	}
	if flag.Arg(0) == "retire" {
		return retireGuest(a, c, *vmid)
	}
	if flag.Arg(0) != "probe" {
		return fmt.Errorf("unknown lab command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var pool struct {
		Members []struct {
			Type string `json:"type"`
		} `json:"members"`
	}
	if err := a.call(ctx, http.MethodGet, "/pools/"+c.Pool, nil, &pool); err != nil {
		return err
	}
	if c.DenyVMID != 0 {
		if c.DenyVMID < 100 {
			return fmt.Errorf("invalid denial-test VM identifier")
		}
		err := a.call(ctx, http.MethodGet, fmt.Sprintf("/nodes/%s/qemu/%d/config", c.Node, c.DenyVMID), nil, nil)
		var denied apiStatusError
		if !errors.As(err, &denied) || denied.Code != http.StatusForbidden {
			return fmt.Errorf("out-of-pool denial check failed")
		}
		fmt.Println("Out-of-pool VM access was denied.")
	}
	fmt.Printf("Authenticated qualification pool probe passed (%d resources).\n", len(pool.Members))
	return nil
}
