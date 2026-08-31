package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

const usage = `winctl — control interface for winunitd

Usage:
  winctl [--help]
  winctl [--user] <command> [args]

Commands:
  start <unit>        Start a unit
  stop <unit>         Stop a unit
  restart <unit>      Restart a unit
  status [unit]       Show machine or unit status
  enable <unit>       Enable a unit
  disable <unit>      Disable a unit
  list-units          List loaded units
  list-timers         List timers
  logs <unit>         Show unit logs
  daemon-reload       Reload unit files
  verify <path|unit>  Verify a unit file path (no daemon) or a loaded unit
  enable-linger <user>
                      Persist a user manager across logoff and at boot
                      (Administrators, system pipe)
  disable-linger <user>
                      Stop lingering for a user (Administrators, system pipe)

Commands other than verify-on-a-file-path talk to winunitd over
\\.\pipe\winunitd\control. --user talks to
\\.\pipe\winunitd\user\<SID>\control for the current user.
enable-linger / disable-linger always use the system pipe.

Flags:
  --user          Talk to the per-user manager (current user SID)
  -h, --help      Show this help
`

const verifyUsage = `winctl verify — check unit files without a running daemon, or a loaded unit

Usage:
  winctl verify <path> [<path> ...]
  winctl verify <unit>

A filesystem path is verified locally (no daemon). A unit name is verified
through winunitd.

Unknown directives are errors. ExecStart must be an absolute Windows path
(SearchPath=no). An omitted WorkingDirectory is a warning; System32 is not
used as a default. Type=scm requires ServiceName= and does not use ExecStart=.
`

type cli struct {
	stdout   io.Writer
	stderr   io.Writer
	dial     func(context.Context) (net.Conn, error)
	userDial func(context.Context) (net.Conn, error)
	user     bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runCLI(args, stdout, stderr, defaultDial)
}

func defaultDial(ctx context.Context) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return protocol.DialDefault(ctx)
}

func defaultUserDial(ctx context.Context) (net.Conn, error) {
	sid, err := protocol.CurrentUserSID()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return protocol.DialUser(ctx, sid)
}

func runCLI(args []string, stdout, stderr io.Writer, dial func(context.Context) (net.Conn, error)) int {
	return runCLIUser(args, stdout, stderr, dial, defaultUserDial)
}

func runCLIUser(args []string, stdout, stderr io.Writer, dial, userDial func(context.Context) (net.Conn, error)) int {
	c := &cli{stdout: stdout, stderr: stderr, dial: dial, userDial: userDial}
	return c.run(args)
}

func (c *cli) run(args []string) int {
	user, rest := splitUserFlag(args)
	c.user = user
	args = rest

	if len(args) == 0 || isHelpFlag(args[0]) {
		fmt.Fprint(c.stdout, usage)
		return 0
	}

	cmd, rest := args[0], args[1:]
	if len(rest) == 1 && isHelpFlag(rest[0]) {
		if cmd == "verify" {
			fmt.Fprint(c.stdout, verifyUsage)
			return 0
		}
		fmt.Fprint(c.stdout, usage)
		return 0
	}

	switch cmd {
	case "verify":
		return c.verify(rest)
	case "start":
		return c.unitCmd(rest, protocol.MethodStart, c.printUnitResult)
	case "stop":
		return c.unitCmd(rest, protocol.MethodStop, c.printUnitResult)
	case "restart":
		return c.unitCmd(rest, protocol.MethodRestart, c.printUnitResult)
	case "status":
		return c.status(rest)
	case "enable":
		return c.unitCmd(rest, protocol.MethodEnable, c.printEnable)
	case "disable":
		return c.unitCmd(rest, protocol.MethodDisable, c.printEnable)
	case "list-units":
		return c.noArg(rest, c.listUnits)
	case "list-timers":
		return c.noArg(rest, c.listTimers)
	case "logs":
		return c.logs(rest)
	case "daemon-reload":
		return c.noArg(rest, c.daemonReload)
	case "enable-linger":
		return c.lingerCmd(rest, protocol.MethodEnableLinger)
	case "disable-linger":
		return c.lingerCmd(rest, protocol.MethodDisableLinger)
	default:
		fmt.Fprintf(c.stderr, "winctl: unknown command %q\n\n", cmd)
		fmt.Fprint(c.stderr, usage)
		return 2
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

func splitUserFlag(args []string) (user bool, rest []string) {
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--user":
			user = true
			i++
		default:
			return user, args[i:]
		}
	}
	return user, nil
}

func (c *cli) call(fn func(context.Context, *protocol.Client) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dial := c.dial
	if c.user {
		dial = c.userDial
		if dial == nil {
			dial = defaultUserDial
		}
	}
	if dial == nil {
		dial = defaultDial
	}
	conn, err := dial(ctx)
	if err != nil {
		if c.user {
			return fmt.Errorf("cannot connect to winunitd user manager: %w", err)
		}
		return fmt.Errorf("cannot connect to winunitd: %w", err)
	}
	defer conn.Close()
	return fn(ctx, protocol.NewClient(conn))
}

func (c *cli) rpcError(err error) int {
	fmt.Fprintf(c.stderr, "winctl: %v\n", err)
	return 1
}

func (c *cli) needUnit(args []string, cmd string) (string, int) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintf(c.stderr, "winctl %s: unit name required\n", cmd)
		return "", 2
	}
	return args[0], 0
}

func (c *cli) unitCmd(args []string, method string, print func(any) int) int {
	name, code := c.needUnit(args, method)
	if code != 0 {
		return code
	}
	var result any
	err := c.call(func(ctx context.Context, cl *protocol.Client) error {
		switch method {
		case protocol.MethodStart:
			out, err := cl.Start(ctx, name)
			result = out
			return err
		case protocol.MethodStop:
			out, err := cl.Stop(ctx, name)
			result = out
			return err
		case protocol.MethodRestart:
			out, err := cl.Restart(ctx, name)
			result = out
			return err
		case protocol.MethodEnable:
			out, err := cl.Enable(ctx, name)
			result = out
			return err
		case protocol.MethodDisable:
			out, err := cl.Disable(ctx, name)
			result = out
			return err
		default:
			return protocol.ErrMethodNotFound(method)
		}
	})
	if err != nil {
		return c.rpcError(err)
	}
	return print(result)
}

func (c *cli) noArg(args []string, fn func() int) int {
	if len(args) != 0 {
		fmt.Fprintf(c.stderr, "winctl: unexpected argument %q\n", args[0])
		return 2
	}
	return fn()
}

func (c *cli) verify(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(c.stderr, "winctl verify: unit file path or unit name required")
		fmt.Fprint(c.stderr, verifyUsage)
		return 2
	}
	if anyLooksLikeFilePath(args) {
		return runVerify(args, c.stdout, c.stderr)
	}
	failed := false
	for _, name := range args {
		var ver *protocol.VerifyResult
		err := c.call(func(ctx context.Context, cl *protocol.Client) error {
			var err error
			ver, err = cl.Verify(ctx, name)
			return err
		})
		if err != nil {
			fmt.Fprintf(c.stderr, "winctl: %v\n", err)
			failed = true
			continue
		}
		for _, iss := range ver.Issues {
			fmt.Fprintln(c.stderr, formatIssue(iss))
		}
		if !ver.OK {
			failed = true
			continue
		}
		fmt.Fprintf(c.stdout, "%s: verified\n", ver.Name)
	}
	if failed {
		return 1
	}
	return 0
}

func looksLikeFilePath(arg string) bool {
	if strings.ContainsAny(arg, `/\`) {
		return true
	}
	if len(arg) >= 2 && arg[1] == ':' {
		return true
	}
	if _, err := os.Stat(arg); err == nil {
		return true
	}
	return false
}

func anyLooksLikeFilePath(args []string) bool {
	for _, a := range args {
		if looksLikeFilePath(a) {
			return true
		}
	}
	return false
}

func formatIssue(iss protocol.Issue) string {
	loc := iss.Path
	if loc == "" {
		loc = "unit"
	}
	if iss.Line > 0 {
		return fmt.Sprintf("%s:%d: %s: %s", loc, iss.Line, iss.Severity, iss.Message)
	}
	return fmt.Sprintf("%s: %s: %s", loc, iss.Severity, iss.Message)
}

func (c *cli) status(args []string) int {
	if len(args) > 1 {
		fmt.Fprintf(c.stderr, "winctl status: unexpected argument %q\n", args[1])
		return 2
	}
	unit := ""
	if len(args) == 1 {
		unit = args[0]
	}
	var st *protocol.StatusResult
	err := c.call(func(ctx context.Context, cl *protocol.Client) error {
		var err error
		st, err = cl.Status(ctx, unit)
		return err
	})
	if err != nil {
		return c.rpcError(err)
	}
	return c.printStatus(st)
}

func (c *cli) listUnits() int {
	var got *protocol.ListUnitsResult
	err := c.call(func(ctx context.Context, cl *protocol.Client) error {
		var err error
		got, err = cl.ListUnits(ctx)
		return err
	})
	if err != nil {
		return c.rpcError(err)
	}
	return c.printListUnits(got)
}

func (c *cli) listTimers() int {
	var got *protocol.ListTimersResult
	err := c.call(func(ctx context.Context, cl *protocol.Client) error {
		var err error
		got, err = cl.ListTimers(ctx)
		return err
	})
	if err != nil {
		return c.rpcError(err)
	}
	return c.printListTimers(got)
}

func (c *cli) logs(args []string) int {
	name, code := c.needUnit(args, "logs")
	if code != 0 {
		return code
	}
	var got *protocol.LogsResult
	err := c.call(func(ctx context.Context, cl *protocol.Client) error {
		var err error
		got, err = cl.Logs(ctx, protocol.LogsParams{Unit: name})
		return err
	})
	if err != nil {
		return c.rpcError(err)
	}
	return c.printLogs(got)
}

func (c *cli) lingerCmd(args []string, method string) int {
	if c.user {
		fmt.Fprintf(c.stderr, "winctl: %s talks to the system pipe (do not use --user)\n", method)
		return 2
	}
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintf(c.stderr, "winctl %s: user name required\n", method)
		return 2
	}
	user := args[0]
	var result *protocol.LingerResult
	err := c.call(func(ctx context.Context, cl *protocol.Client) error {
		var err error
		switch method {
		case protocol.MethodEnableLinger:
			result, err = cl.EnableLinger(ctx, user)
		case protocol.MethodDisableLinger:
			result, err = cl.DisableLinger(ctx, user)
		default:
			err = protocol.ErrMethodNotFound(method)
		}
		return err
	})
	if err != nil {
		return c.rpcError(err)
	}
	return c.printLinger(result)
}

func (c *cli) printLinger(r *protocol.LingerResult) int {
	if r == nil {
		return 0
	}
	who := r.SID
	if r.User != "" {
		who = r.User + " (" + r.SID + ")"
	}
	if r.Lingering {
		fmt.Fprintf(c.stdout, "%s: lingering\n", who)
		return 0
	}
	fmt.Fprintf(c.stdout, "%s: linger disabled\n", who)
	return 0
}

func (c *cli) daemonReload() int {
	var got *protocol.DaemonReloadResult
	err := c.call(func(ctx context.Context, cl *protocol.Client) error {
		var err error
		got, err = cl.DaemonReload(ctx)
		return err
	})
	if err != nil {
		return c.rpcError(err)
	}
	for _, e := range got.Errors {
		fmt.Fprintln(c.stderr, e)
	}
	if got.Cycle != "" {
		fmt.Fprintf(c.stderr, "ordering cycle: %s\n", got.Cycle)
	}
	fmt.Fprintf(c.stdout, "Reloaded %d units.\n", got.Loaded)
	if len(got.Errors) > 0 || got.Cycle != "" {
		return 1
	}
	return 0
}

func (c *cli) printUnitResult(v any) int {
	r, ok := v.(*protocol.UnitResult)
	if !ok || r == nil {
		return 0
	}
	fmt.Fprintf(c.stdout, "%s: %s\n", r.Unit, r.ActiveState)
	return 0
}

func (c *cli) printEnable(v any) int {
	r, ok := v.(*protocol.EnableResult)
	if !ok || r == nil {
		return 0
	}
	state := "disabled"
	if r.Enabled {
		state = "enabled"
	}
	if len(r.Targets) > 0 {
		fmt.Fprintf(c.stdout, "%s: %s (%s)\n", r.Unit, state, strings.Join(r.Targets, ", "))
		return 0
	}
	fmt.Fprintf(c.stdout, "%s: %s\n", r.Unit, state)
	return 0
}

func (c *cli) printStatus(st *protocol.StatusResult) int {
	if st.Machine != nil {
		m := st.Machine
		fmt.Fprintf(c.stdout, "winunitd\n")
		fmt.Fprintf(c.stdout, "  State: %s\n", m.State)
		fmt.Fprintf(c.stdout, "  Units: %d loaded\n", m.UnitsLoaded)
		fmt.Fprintf(c.stdout, "         %d active\n", m.UnitsActive)
		fmt.Fprintf(c.stdout, "         %d failed\n", m.UnitsFailed)
		fmt.Fprintf(c.stdout, "  Timers: %d loaded\n", m.TimersLoaded)
		if m.UserManagers > 0 || m.Lingering > 0 {
			fmt.Fprintf(c.stdout, "  Users:  %d managers\n", m.UserManagers)
			fmt.Fprintf(c.stdout, "          %d lingering\n", m.Lingering)
		}
		return 0
	}
	if st.Unit != nil {
		u := st.Unit
		title := u.Name
		if u.Description != "" {
			title = u.Name + " - " + u.Description
		}
		enabled := "disabled"
		if u.Enabled {
			enabled = "enabled"
		}
		fmt.Fprintf(c.stdout, "● %s\n", title)
		fmt.Fprintf(c.stdout, "     Loaded: %s (%s; %s)\n", u.LoadState, u.Path, enabled)
		fmt.Fprintf(c.stdout, "     Active: %s\n", u.ActiveState)
		if u.MainPID != 0 {
			fmt.Fprintf(c.stdout, "   Main PID: %d\n", u.MainPID)
		}
		if u.InvocationID != "" {
			fmt.Fprintf(c.stdout, "InvocationID=%s\n", u.InvocationID)
		}
		if u.Error != "" {
			fmt.Fprintf(c.stdout, "      Error: %s\n", u.Error)
		}
		if u.Next != "" {
			fmt.Fprintf(c.stdout, "       Next: %s\n", u.Next)
		}
		if u.Last != "" {
			fmt.Fprintf(c.stdout, "       Last: %s\n", u.Last)
		}
		return 0
	}
	return 0
}

func (c *cli) printListUnits(got *protocol.ListUnitsResult) int {
	fmt.Fprintf(c.stdout, "%-28s %-8s %-12s %-8s %-8s %s\n", "UNIT", "LOAD", "ACTIVE", "ENABLED", "PID", "DESCRIPTION")
	for _, u := range got.Units {
		en := "no"
		if u.Enabled {
			en = "yes"
		}
		pid := "-"
		if u.MainPID != 0 {
			pid = fmt.Sprintf("%d", u.MainPID)
		}
		fmt.Fprintf(c.stdout, "%-28s %-8s %-12s %-8s %-8s %s\n", u.Name, u.LoadState, u.ActiveState, en, pid, u.Description)
	}
	return 0
}

func (c *cli) printListTimers(got *protocol.ListTimersResult) int {
	fmt.Fprintf(c.stdout, "%-28s %-20s %-24s %-24s %-12s %-8s\n", "TIMER", "ACTIVATES", "NEXT", "LAST", "ACTIVE", "ENABLED")
	for _, u := range got.Timers {
		en := "no"
		if u.Enabled {
			en = "yes"
		}
		next := u.Next
		if next == "" {
			next = "-"
		}
		last := u.Last
		if last == "" {
			last = "-"
		}
		fmt.Fprintf(c.stdout, "%-28s %-20s %-24s %-24s %-12s %-8s\n", u.Name, u.Unit, next, last, u.ActiveState, en)
	}
	return 0
}

func (c *cli) printLogs(got *protocol.LogsResult) int {
	if len(got.Entries) == 0 {
		fmt.Fprintf(c.stdout, "No journal entries for %s.\n", got.Unit)
		return 0
	}
	for _, e := range got.Entries {
		fmt.Fprintln(c.stdout, formatLogEntry(e))
	}
	return 0
}

func formatLogEntry(e protocol.LogEntry) string {
	ts := e.Timestamp
	if t, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
		ts = t.Local().Format("Jan 02 15:04:05")
	} else if t, err := time.Parse(time.RFC3339, e.Timestamp); err == nil {
		ts = t.Local().Format("Jan 02 15:04:05")
	}
	name := e.Unit
	if name == "" {
		name = "unit"
	} else {
		name = strings.TrimSuffix(name, ".service")
	}
	if ts == "" {
		if e.PID != 0 {
			return fmt.Sprintf("%s[%d]: %s", name, e.PID, e.Message)
		}
		return e.Message
	}
	if e.PID != 0 {
		return fmt.Sprintf("%s %s[%d]: %s", ts, name, e.PID, e.Message)
	}
	return fmt.Sprintf("%s %s: %s", ts, name, e.Message)
}
