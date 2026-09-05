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
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lab:", err)
		os.Exit(1)
	}
}

func run() error {
	file := flag.String("config", "", "private controller configuration")
	flag.Parse()
	if *file == "" || flag.NArg() != 1 || flag.Arg(0) != "probe" {
		return fmt.Errorf("usage: lab -config PRIVATE_FILE probe")
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
