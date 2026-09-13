package main

import (
	"fmt"
	"io"
	"os"

	"github.com/PLN/winunitd/internal/unit"
)

const migrateUsage = `winctl migrate --file PATH [--output NEWPATH]

Preview an effective-behavior-preserving conversion to FormatVersion=2.
Without --output, print the converted UTF-8 file; changes go to stderr.
With --output, create a new file. Existing files are never overwritten.
The source is unchanged; this command does not contact a manager or reload units.
`

func (c *cli) migrate(args []string) int {
	var path, output string
	for len(args) > 0 {
		if len(args) < 2 {
			fmt.Fprint(c.stderr, migrateUsage)
			return 2
		}
		switch args[0] {
		case "--file":
			if path != "" {
				fmt.Fprint(c.stderr, migrateUsage)
				return 2
			}
			path = args[1]
		case "--output":
			if output != "" {
				fmt.Fprint(c.stderr, migrateUsage)
				return 2
			}
			output = args[1]
		default:
			fmt.Fprint(c.stderr, migrateUsage)
			return 2
		}
		args = args[2:]
	}
	if path == "" {
		fmt.Fprint(c.stderr, migrateUsage)
		return 2
	}
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(c.stderr, "winctl migrate: %v\n", err)
		return 1
	}
	src, err := io.ReadAll(io.LimitReader(f, unit.MaxFileBytes+1))
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		fmt.Fprintf(c.stderr, "winctl migrate: %v\n", err)
		return 1
	}
	preview := unit.ConvertToV2(path, unit.UnitNameFromPath(path), src)
	for _, issue := range preview.Issues {
		fmt.Fprintln(c.stderr, issue.String())
	}
	if preview.Output == nil {
		return 1
	}
	for _, change := range preview.Changes {
		fmt.Fprintln(c.stderr, "winctl migrate: "+change)
	}
	if output == "" {
		if _, err := c.stdout.Write(preview.Output); err != nil {
			fmt.Fprintf(c.stderr, "winctl migrate: %v\n", err)
			return 1
		}
		return 0
	}
	f, err = os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintf(c.stderr, "winctl migrate: %v\n", err)
		return 1
	}
	_, err = f.Write(preview.Output)
	if err == nil {
		err = f.Sync()
	}
	closeErr = f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		fmt.Fprintf(c.stderr, "winctl migrate: output write failed; inspect %s: %v\n", output, err)
		return 1
	}
	fmt.Fprintf(c.stdout, "Created %s\n", output)
	return 0
}
