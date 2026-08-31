package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/runtime"
)

const usage = `winunitd — Windows unit manager daemon

Usage:
  winunitd [--base-dir DIR]
  winunitd install [--base-dir DIR]
  winunitd uninstall

Runs as the Windows Service "winunitd" (DisplayName: WinUnit Manager) when
started by SCM: LocalSystem, Automatic (Delayed Start), restart on failure.
Preshutdown is accepted so ordered stop can be added later (M10).

Console mode (no SCM) is used for tests and local runs. --base-dir still
applies. A daemon-level Job Object enforces strict ownership: if this
process dies, assigned children die with it (DESIGN.md §66). Per-unit jobs
and CreateProcess are M5.

Listens on \\.\pipe\winunitd\control (LocalSystem and Administrators only).

Flags:
  --base-dir DIR   Data directory (units\, enabled\). Default: %ProgramData%\winunitd
  -h, --help       Show this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	for _, a := range args {
		switch a {
		case "-h", "-help", "--help":
			fmt.Fprint(stdout, usage)
			return 0
		}
	}

	cmd := ""
	flagArgs := args
	if len(args) > 0 {
		switch args[0] {
		case "install", "uninstall":
			cmd = args[0]
			flagArgs = args[1:]
		}
	}

	fs := flag.NewFlagSet("winunitd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	baseDir := fs.String("base-dir", manager.DefaultBaseDir(), "data directory")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "winunitd: unexpected argument %q\n", fs.Arg(0))
		fmt.Fprint(stderr, usage)
		return 2
	}

	switch cmd {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(stderr, "winunitd: %v\n", err)
			return 1
		}
		if err := runtime.Install(exe, *baseDir); err != nil {
			fmt.Fprintf(stderr, "winunitd: install: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "winunitd: installed service %s (%s)\n", runtime.ServiceName, runtime.DisplayName)
		return 0
	case "uninstall":
		if err := runtime.Uninstall(); err != nil {
			fmt.Fprintf(stderr, "winunitd: uninstall: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "winunitd: removed service %s\n", runtime.ServiceName)
		return 0
	}

	asService, err := runtime.RunningAsService()
	if err != nil {
		fmt.Fprintf(stderr, "winunitd: %v\n", err)
		return 1
	}
	if asService {
		err := runtime.RunHost(func(ctx context.Context) error {
			return serve(ctx, *baseDir, stderr)
		})
		if err != nil {
			fmt.Fprintf(stderr, "winunitd: %v\n", err)
			return 1
		}
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := serve(ctx, *baseDir, stderr); err != nil && ctx.Err() == nil {
		fmt.Fprintf(stderr, "winunitd: %v\n", err)
		return 1
	}
	return 0
}
