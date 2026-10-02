package nestedjob

import (
	"encoding/json"
	"fmt"
	"io"
)

// HelperSelector is the TestMain argument that routes a native test binary to
// Main. Arguments after it form the fixture command line.
const HelperSelector = "-winunitd-helper=nested-job"

// HelperArgs returns the fixture arguments after HelperSelector, or false.
func HelperArgs(args []string) ([]string, bool) {
	for i, a := range args {
		if a == HelperSelector {
			return args[i+1:], true
		}
	}
	return nil, false
}

// Main runs one fixture role and returns its exit code. prefix holds the
// arguments that select the fixture when this image is invoked again (a test
// binary's HelperSelector); every child receives the same prefix.
func Main(prefix, args []string, stdout, stderr io.Writer) int {
	inv, err := Parse(args)
	if err != nil {
		fmt.Fprintln(stderr, "nested-job:", err)
		return 2
	}
	if inv.Matrix != nil {
		if err := printMatrix(*inv.Matrix, stdout); err != nil {
			fmt.Fprintln(stderr, "nested-job:", err)
			return 1
		}
		return 0
	}
	if err := run(inv, prefix); err != nil {
		fmt.Fprintln(stderr, "nested-job:", err)
		return 1
	}
	return 0
}

// printMatrix writes the selected expanded executions as JSON lines.
func printMatrix(c MatrixConfig, w io.Writer) error {
	m, err := CaseMatrix()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	for _, e := range m.Expand() {
		if c.Identity != "" && e.Identity != c.Identity {
			continue
		}
		if c.Lane != "" {
			found := false
			for _, lane := range e.Lanes {
				found = found || lane == c.Lane
			}
			if !found {
				continue
			}
		}
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}
