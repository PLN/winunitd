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
	if inv.Summarize != nil {
		return summarize(*inv.Summarize, stdout, stderr)
	}
	if inv.Record != nil {
		if err := runRecord(*inv.Record); err != nil {
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

// printMatrix writes the selected executions as JSON lines, or the hash.
func printMatrix(c MatrixConfig, w io.Writer) error {
	m, err := CaseMatrix()
	if err != nil {
		return err
	}
	if c.Hash {
		_, err := fmt.Fprintln(w, MatrixHash())
		return err
	}
	enc := json.NewEncoder(w)
	for _, e := range m.Select(c.Select) {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

// Summary exit codes: complete acceptance, any failure or incompleteness,
// and a development selection whose selected executions all passed.
const (
	SummaryComplete = 0
	SummaryFailed   = 1
	SummaryPartial  = 3
)

func summarize(c SummarizeConfig, stdout, stderr io.Writer) int {
	m, err := CaseMatrix()
	if err != nil {
		fmt.Fprintln(stderr, "nested-job:", err)
		return SummaryFailed
	}
	run, err := LoadAdmission(c.Admission)
	if err != nil {
		fmt.Fprintln(stderr, "nested-job:", err)
		return SummaryFailed
	}
	results, err := ReadResults(c.Results)
	if err != nil {
		fmt.Fprintln(stderr, "nested-job:", err)
		return SummaryFailed
	}
	s := Summarize(m, results, run, c.Select)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return SummaryFailed
	}
	switch {
	case s.Complete:
		return SummaryComplete
	case s.Partial && s.Passed == s.Selected && len(s.Problems) == 0:
		fmt.Fprintln(stderr, "nested-job: partial qualification; #265 acceptance stays open")
		return SummaryPartial
	default:
		return SummaryFailed
	}
}
