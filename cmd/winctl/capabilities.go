package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/PLN/winunitd/internal/protocol"
)

const capabilitiesUsage = `winctl capabilities — report the manager build and the contracts it enforces

Usage:
  winctl capabilities [--require NAME[,NAME...]]...
  winctl --user capabilities [--require NAME[,NAME...]]...

Prints the answering manager's version, source commit, protocol methods,
accepted FormatVersion values, features, job limits, user-manager modes, and
recognized directives as JSON. No unit is started or queried. winctl
--version reports only this winctl executable.

A feature is listed only when the build enforces it. Features describe the
build, not qualification. --require exits 1 unless every named feature is
listed; the JSON is still printed. A manager that predates this query answers
method-not-found; winctl then reports that it is below any capability floor
and exits 1. Usage errors exit 2.
`

func (c *cli) capabilities(args []string) int {
	var required []string
	for i := 0; i < len(args); i++ {
		var names string
		switch a := args[i]; {
		case a == "--require":
			if i+1 == len(args) {
				fmt.Fprintln(c.stderr, "winctl capabilities: --require needs a feature name")
				return 2
			}
			i++
			names = args[i]
		case strings.HasPrefix(a, "--require="):
			names = strings.TrimPrefix(a, "--require=")
		default:
			fmt.Fprintf(c.stderr, "winctl capabilities: unexpected argument %q\n", a)
			fmt.Fprint(c.stderr, capabilitiesUsage)
			return 2
		}
		for _, name := range strings.Split(names, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				fmt.Fprintln(c.stderr, "winctl capabilities: empty feature name")
				return 2
			}
			required = append(required, name)
		}
	}
	var got *protocol.CapabilitiesResult
	err := c.call(func(ctx context.Context, cl *protocol.Client) error {
		var err error
		got, err = cl.Capabilities(ctx)
		return err
	})
	if err != nil {
		if protocolCode(err) == protocol.CodeMethodNotFound {
			fmt.Fprintf(c.stderr, "winctl: %v\n", err)
			fmt.Fprintln(c.stderr, "winctl capabilities: the manager predates the capability query; treat it as below any capability floor")
			return 1
		}
		return c.rpcError(err)
	}
	encoder := json.NewEncoder(c.stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(got); err != nil {
		return c.rpcError(err)
	}
	var missing []string
	for _, name := range required {
		if !slices.Contains(got.Features, name) && !slices.Contains(missing, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(c.stderr, "winctl capabilities: missing required feature(s): %s\n", strings.Join(missing, ", "))
		return 1
	}
	return 0
}
