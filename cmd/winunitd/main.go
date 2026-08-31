package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
)

const usage = `winunitd — Windows unit manager daemon

Usage:
  winunitd [--base-dir DIR]

Listens on \\.\pipe\winunitd\control (LocalSystem and Administrators only).
This process is not registered with the Service Control Manager yet.

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

	fs := flag.NewFlagSet("winunitd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	baseDir := fs.String("base-dir", manager.DefaultBaseDir(), "data directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "winunitd: unexpected argument %q\n", fs.Arg(0))
		fmt.Fprint(stderr, usage)
		return 2
	}

	m, err := manager.New(manager.Config{BaseDir: *baseDir})
	if err != nil {
		fmt.Fprintf(stderr, "winunitd: %v\n", err)
		return 1
	}
	rel, err := m.Reload()
	if err != nil {
		fmt.Fprintf(stderr, "winunitd: load units: %v\n", err)
		return 1
	}
	for _, e := range rel.Errors {
		fmt.Fprintf(stderr, "winunitd: %s\n", e)
	}
	if rel.Cycle != "" {
		fmt.Fprintf(stderr, "winunitd: ordering cycle: %s\n", rel.Cycle)
	}

	lis, err := protocol.ListenControl()
	if err != nil {
		fmt.Fprintf(stderr, "winunitd: listen: %v\n", err)
		return 1
	}
	defer lis.Close()
	fmt.Fprintf(stderr, "winunitd: loaded %d units, listening on %s\n", rel.Loaded, protocol.DefaultPipeName)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := protocol.Serve(ctx, lis, m, protocol.DefaultAuthorizer()); err != nil && ctx.Err() == nil {
		fmt.Fprintf(stderr, "winunitd: %v\n", err)
		return 1
	}
	return 0
}
