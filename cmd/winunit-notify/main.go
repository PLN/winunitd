package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/PLN/winunitd/internal/notify"
)

const usage = `winunit-notify — send READY / WATCHDOG / STATUS to winunitd

Usage:
  winunit-notify --ready
  winunit-notify --watchdog
  winunit-notify --status TEXT

Reads WINUNIT_NOTIFY_PIPE from the environment (injected by winunitd).
Multiple flags may be combined into one payload.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("winunit-notify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	ready := fs.Bool("ready", false, "send READY=1")
	watchdog := fs.Bool("watchdog", false, "send WATCHDOG=1")
	status := fs.String("status", "", "send STATUS=TEXT")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if !*ready && !*watchdog && *status == "" {
		fmt.Fprint(stderr, usage)
		return 2
	}

	addr, err := notify.PipeFromGetenv(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "winunit-notify: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg := notify.Message{Ready: *ready, Watchdog: *watchdog, Status: *status}
	if err := notify.Send(ctx, addr, msg); err != nil {
		fmt.Fprintf(stderr, "winunit-notify: %v\n", err)
		return 1
	}
	_ = stdout
	return 0
}
