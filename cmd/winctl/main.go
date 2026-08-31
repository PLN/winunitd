package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const usage = `winctl — control interface for winunitd

Usage:
  winctl [--help]
  winctl <command> [args]

Commands:
  verify          Verify unit files (no daemon required)

Planned commands (not implemented):
  start           Start a unit
  stop            Stop a unit
  restart         Restart a unit
  status          Show unit status
  enable          Enable a unit
  disable         Disable a unit
  list-units      List loaded units
  list-timers     List timers
  logs            Show unit logs
  daemon-reload   Reload unit files

Flags:
  -h, --help      Show this help

verify does not talk to winunitd. Other commands are still placeholders.
`

const verifyUsage = `winctl verify — check unit files without a running daemon

Usage:
  winctl verify <path> [<path> ...]

Unknown directives are errors. ExecStart must be an absolute Windows path
(SearchPath=no). An omitted WorkingDirectory is a warning; System32 is not
used as a default.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelpFlag(args[0]) {
		fmt.Fprint(stdout, usage)
		return 0
	}

	switch args[0] {
	case "verify":
		rest := args[1:]
		if len(rest) == 1 && isHelpFlag(rest[0]) {
			fmt.Fprint(stdout, verifyUsage)
			return 0
		}
		return runVerify(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "winctl: %q is not implemented yet\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return 0
	}
}

func isHelpFlag(s string) bool {
	switch strings.TrimSpace(s) {
	case "-h", "-help", "--help", "help":
		return true
	default:
		return false
	}
}
