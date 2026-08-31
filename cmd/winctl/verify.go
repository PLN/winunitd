package main

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/PLN/winunitd/internal/unit"
)

func runVerify(paths []string, stdout, stderr io.Writer) int {
	if len(paths) == 0 {
		fmt.Fprintln(stderr, "winctl verify: unit file path required")
		fmt.Fprint(stderr, verifyUsage)
		return 2
	}

	failed := false
	for _, p := range paths {
		rep := unit.VerifyPath(p)
		for _, iss := range rep.Issues {
			fmt.Fprintln(stdout, iss.String())
		}
		if rep.HasError() {
			failed = true
			continue
		}
		name := filepath.Base(p)
		if name == "" || name == "." {
			name = p
		}
		fmt.Fprintf(stdout, "%s: verified\n", name)
	}
	if failed {
		return 1
	}
	return 0
}
