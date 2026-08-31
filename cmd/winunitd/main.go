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
	"github.com/PLN/winunitd/internal/runtime"
)

const usage = `winunitd — Windows unit manager daemon

Usage:
  winunitd [--base-dir DIR]
  winunitd --user-manager SID [--base-dir DIR]
  winunitd install [--base-dir DIR]
  winunitd uninstall

Runs as the Windows Service "winunitd" (DisplayName: WinUnit Manager) when
started by SCM: LocalSystem, Automatic (Delayed Start), restart on failure.
Preshutdown is accepted and used for ordered stop (DESIGN.md §42).

Console mode (no SCM) is used for tests and local runs. --base-dir still
applies. A daemon-level Job Object enforces strict ownership: if this
process dies, assigned children die with it (DESIGN.md §66). Each started
unit gets its own nested Job Object; winctl start CreateProcess's ExecStart
into that job. On start (SCM or console) the daemon starts default.target,
which Wants=timers.target (enabled timers arm) and pulls in enabled
Wants=/Requires= only. Timers are scheduled internally (OnBootSec since
machine boot, OnStartupSec since this process).

On first interactive logon the system manager launches
winunitd --user-manager <SID> (same binary, not an SCM service) using
WTSQueryUserToken only. If a token cannot be obtained, that user manager
is not started (fail closed; no stored credentials or alternate logon). User
units load from %LOCALAPPDATA%\winunitd\units\ and are controlled on
\\.\pipe\winunitd\user\<SID>\control. Logoff with no linger kills the
user manager. System list-units does not show user units.

On SCM stop, preshutdown, or console SIGINT, units stop in reverse
After=/Before= order (shutdown.target as the stop root), then the daemon
Job Object is closed so children cannot outlive winunitd.exe.

System manager listens on \\.\pipe\winunitd\control (LocalSystem and
Administrators only).

Flags:
  --base-dir DIR         Data directory (units\, enabled\, journal\, runtime\).
                         Default: %ProgramData%\winunitd (system) or
                         %LOCALAPPDATA%\winunitd (user manager)
  --user-manager SID     Run as the per-user manager for SID (not an SCM service)
  -h, --help             Show this help
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
	userSID := fs.String("user-manager", "", "run as per-user manager for SID")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "winunitd: unexpected argument %q\n", fs.Arg(0))
		fmt.Fprint(stderr, usage)
		return 2
	}

	baseSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "base-dir" {
			baseSet = true
		}
	})

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

	if *userSID != "" {
		if !protocol.ValidSID(*userSID) {
			fmt.Fprintf(stderr, "winunitd: invalid SID %q\n", *userSID)
			return 2
		}
		dir := *baseDir
		if !baseSet {
			dir = ""
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if err := serveUser(ctx, *userSID, dir, stderr); err != nil && ctx.Err() == nil {
			fmt.Fprintf(stderr, "winunitd: %v\n", err)
			return 1
		}
		return 0
	}

	asService, err := runtime.RunningAsService()
	if err != nil {
		fmt.Fprintf(stderr, "winunitd: %v\n", err)
		return 1
	}
	if asService {
		ch := make(chan runtime.SessionChange, 32)
		err := runtime.RunHostNotify(func(ctx context.Context) error {
			return serve(ctx, *baseDir, stderr, ch)
		}, func(sc runtime.SessionChange) {
			select {
			case ch <- sc:
			default:
			}
		})
		if err != nil {
			fmt.Fprintf(stderr, "winunitd: %v\n", err)
			return 1
		}
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ch := make(chan runtime.SessionChange, 32)
	if err := serve(ctx, *baseDir, stderr, ch); err != nil && ctx.Err() == nil {
		fmt.Fprintf(stderr, "winunitd: %v\n", err)
		return 1
	}
	return 0
}
