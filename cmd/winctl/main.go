package main

import (
	"fmt"
	"os"
	"strings"
)

const usage = `winctl — control interface for winunitd

Usage:
  winctl [--help]
  winctl <command> [args]

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
  verify          Verify unit files

Flags:
  -h, --help      Show this help

This CLI is a scaffold placeholder and does not talk to winunitd yet.
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 || isHelp(args) {
		fmt.Fprint(os.Stdout, usage)
		os.Exit(0)
	}

	fmt.Fprintf(os.Stderr, "winctl: %q is not implemented yet\n\n", args[0])
	fmt.Fprint(os.Stderr, usage)
	os.Exit(0)
}

func isHelp(args []string) bool {
	for _, a := range args {
		switch strings.TrimSpace(a) {
		case "-h", "-help", "--help", "help":
			return true
		}
	}
	return false
}
