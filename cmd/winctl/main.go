package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
	"github.com/PLN/winunitd/internal/version"
)

const usage = `winctl — control interface for winunitd

Usage:
  winctl [--help]
  winctl [--version]
  winctl [--user] <command> [args]
  winctl <command> [--user] [args]

Commands:
  start <unit>        Start a unit
  stop <unit>         Stop a unit
  restart <unit>      Restart a unit
  status [unit]       Show machine or unit status
                      (exit 0 active, 3 inactive/failed, 4 not loaded)
  enable <unit>       Enable a unit
  disable <unit>      Disable a unit
  list-units          List loaded units
  snapshot            Print one manager-local lifecycle/operation snapshot as JSON
  list-timers         List timers
  logs <unit> [--follow] [--since <when>]
                      Show unit logs (poll --follow; --since RFC3339 / 1d / "1 hour ago")
  daemon-reload       Reload unit files
  operation ID        Query a retained start/stop/restart outcome
  cancel ID           Cancel unfinished start/restart work
  maintenance [--timeout DURATION]
                      Quiesce system and user workloads until manager restart
                      (Administrators, reserved maintenance pipe; default/max 180s)
  verify <path|unit>  Verify a unit file path (no daemon) or a loaded unit
  enable-linger <user>
                      Persist a user manager across logoff and at boot
                      (Administrators, system pipe)
  disable-linger <user>
                      Stop lingering for a user (Administrators, system pipe)

Commands other than verify-on-a-file-path talk to winunitd over
\\.\pipe\winunitd\control. --user talks to
\\.\pipe\winunitd\user\<SID>\control for the current user.
--user is accepted before or after the verb. enable-linger /
disable-linger always use the system pipe. Maintenance uses the separate
\\.\pipe\winunitd\maintenance endpoint so ordinary connections cannot consume
its capacity; the broker and client must both support this endpoint.

Flags:
  --user          Talk to the per-user manager (current user SID)
  -h, --help      Show this help
  --version       Print the binary version and exit
`

const statusUsage = `winctl status — show machine or unit status

Usage:
  winctl status [unit]
  winctl status [--user] [unit]
  winctl --user status [unit]

No unit prints machine status. A unit name prints that unit.

Exit codes for unit status (systemctl-shaped):
  0  unit is active (running, or active oneshot success)
  3  unit is inactive or failed (loaded but not active)
  4  unit is not loaded / not found

Transport and protocol errors keep their existing non-zero exit
(they are not mapped to 3). Machine status (no unit) exits 0 on success.
`

const verifyUsage = `winctl verify — check unit files without a running daemon, or a loaded unit

Usage:
  winctl verify <path> [<path> ...]
  winctl verify [--file|-f] <path> [<path> ...]
  winctl verify <unit> [<unit> ...]

A filesystem path is verified locally (no daemon). A unit name is verified
through winunitd.

Path vs unit is by form, not by whether a file exists in the current
directory. A unit name has no "/", "\", or drive prefix (foo.service).
A path is explicit: ./foo.service, .\foo.service, or C:\path\foo.service.
--file / -f treats every operand as a path (including a bare foo.service).
Mixing unit names and paths in one invocation is a usage error.

Unknown directives are errors. ExecStart must be an absolute Windows path
(SearchPath=no). An omitted WorkingDirectory is a warning; System32 is not
used as a default. Type=scm requires ServiceName= and does not use ExecStart=.
Type=scheduled-task requires TaskName= and rejects ExecStart= / ExecStartArg=.
MemoryMax= accepts K/M/G (e.g. 2G). ProcessLimit= must be a positive integer.
PriorityClass= is idle, below-normal, normal, above-normal, or high (not realtime).
CPUWeight= is an integer 1–10000. CPUQuota= is N% of total machine CPU with N in 1–100 (trailing % required).
CPUWeight= and CPUQuota= cannot both be set. IoPriority= is idle, low, normal, or high.
Those keys are [Service] only.

A foo.registry unit watches [Registry] RegistryChanged= (HKLM\\... or HKCU\\...
only; no PowerShell drive) and activates foo.service by basename. Path verify
checks the companion .service next to the file. System-scope verify rejects
HKCU (LocalSystem hive); winctl --user verify accepts HKCU and HKLM.

A foo.eventlog unit watches [EventLog] EventLogTrigger= (<Channel>:EventID=<uint16>
only) and activates foo.service by basename. Path verify checks the companion
.service next to the file. winctl --user verify rejects System and Security;
Application and custom log names are allowed.

A foo.path unit watches [Path] PathChanged= (repeatable OR) and/or PathExists=
(repeatable AND; a lock versus systemd OR). Absolute Windows paths only.
PathChanged= watches are non-recursive (this directory only). A file path
watches the parent directory and filters by name. PathExists= starts the
counterpart when every listed path exists (creation can satisfy while active;
deletion does not stop a running counterpart). Mixing both: either may start.
Path verify checks the companion .service next to the file.
`

type cli struct {
	stdout     io.Writer
	stderr     io.Writer
	dial       func(context.Context) (net.Conn, error)
	userDial   func(context.Context) (net.Conn, error)
	user       bool
	followStop <-chan struct{} // tests: close to end --follow after a poll
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runCLI(args, stdout, stderr, nil)
}

func defaultMaintenanceDial(ctx context.Context) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return protocol.DialMaintenance(ctx)
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
	user, rest := extractUserFlag(args)
	c.user = user
	args = rest

	if len(args) == 0 || isHelpFlag(args[0]) {
		fmt.Fprint(c.stdout, usage)
		return 0
	}
	if isVersionFlag(args[0]) {
		fmt.Fprintf(c.stdout, "winctl %s\n", version.Version)
		return 0
	}

	cmd, rest := args[0], args[1:]
	if len(rest) == 1 && isHelpFlag(rest[0]) {
		switch cmd {
		case "verify":
			fmt.Fprint(c.stdout, verifyUsage)
		case "status":
			fmt.Fprint(c.stdout, statusUsage)
		default:
			fmt.Fprint(c.stdout, usage)
		}
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
	case "operation":
		return c.operation(rest, false)
	case "cancel":
		return c.operation(rest, true)
	case "maintenance":
		return c.maintenance(rest)
	case "enable":
		return c.unitCmd(rest, protocol.MethodEnable, c.printEnable)
	case "disable":
		return c.unitCmd(rest, protocol.MethodDisable, c.printEnable)
	case "list-units":
		return c.noArg(rest, c.listUnits)
	case "snapshot":
		return c.noArg(rest, c.snapshot)
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

func isVersionFlag(s string) bool {
	switch strings.TrimSpace(s) {
	case "-version", "--version":
		return true
	default:
		return false
	}
}

// extractUserFlag pulls --user from anywhere in argv (before or after the
// verb). Duplicate --user is accepted. The flag is not a value for other
// options (--since --user would lose the since operand).
func extractUserFlag(args []string) (user bool, rest []string) {
	rest = make([]string, 0, len(args))
	for _, a := range args {
		if a == "--user" {
			user = true
			continue
		}
		rest = append(rest, a)
	}
	return user, rest
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
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.OperationID != "" {
		fmt.Fprintf(c.stderr, "OperationID=%s\n", pe.OperationID)
	}
	return 1
}

func (c *cli) maintenance(args []string) int {
	if c.user {
		fmt.Fprintln(c.stderr, "winctl maintenance: system manager only")
		return 2
	}
	timeout := time.Duration(protocol.MaxMaintenanceTimeoutMS) * time.Millisecond
	if len(args) != 0 {
		if len(args) != 2 || args[0] != "--timeout" {
			fmt.Fprintln(c.stderr, "winctl maintenance: expected --timeout DURATION")
			return 2
		}
		parsed, err := time.ParseDuration(args[1])
		if err != nil || parsed < time.Millisecond || parsed > timeout {
			fmt.Fprintln(c.stderr, "winctl maintenance: timeout must be between 1ms and 180s")
			return 2
		}
		timeout = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()
	dial := c.dial
	if dial == nil {
		dial = defaultMaintenanceDial
	}
	conn, err := dial(ctx)
	if err != nil {
		return c.rpcError(err)
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return c.rpcError(err)
	}
	result, err := protocol.NewClient(conn).Maintenance(ctx, protocol.MaintenanceParams{TimeoutMS: timeout.Milliseconds()})
	if err != nil {
		return c.rpcError(err)
	}
	if result.State != "quiesced" {
		return c.rpcError(fmt.Errorf("maintenance did not confirm quiescence: %s", result.State))
	}
	fmt.Fprintln(c.stdout, "Maintenance: quiesced; restart the manager to resume enabled workloads")
	return 0
}

func (c *cli) needUnit(args []string, cmd string) (string, int) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintf(c.stderr, "winctl %s: unit name required\n", cmd)
		return "", 2
	}
	return unit.NormalizeName(args[0]), 0
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
	va, errMsg := parseVerifyArgs(args)
	if errMsg == "help" {
		fmt.Fprint(c.stdout, verifyUsage)
		return 0
	}
	if errMsg != "" {
		fmt.Fprintf(c.stderr, "winctl verify: %s\n", errMsg)
		fmt.Fprint(c.stderr, verifyUsage)
		return 2
	}
	if len(va.operands) == 0 {
		fmt.Fprintln(c.stderr, "winctl verify: unit file path or unit name required")
		fmt.Fprint(c.stderr, verifyUsage)
		return 2
	}
	paths, units, errMsg := classifyVerifyOperands(va.operands, va.fileFlag)
	if errMsg != "" {
		fmt.Fprintf(c.stderr, "winctl verify: %s\n", errMsg)
		return 2
	}
	if len(paths) > 0 {
		return runVerify(paths, c.user, c.stdout, c.stderr)
	}
	failed := false
	for _, name := range units {
		name = unit.NormalizeName(name)
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

type verifyArgs struct {
	fileFlag bool
	operands []string
}

func parseVerifyArgs(args []string) (verifyArgs, string) {
	var va verifyArgs
	for _, a := range args {
		switch {
		case a == "-h" || a == "-help" || a == "--help" || a == "help":
			return va, "help"
		case a == "--file" || a == "-f":
			va.fileFlag = true
		case strings.HasPrefix(a, "-"):
			return va, fmt.Sprintf("unknown flag %q", a)
		default:
			va.operands = append(va.operands, a)
		}
	}
	return va, ""
}

// isExplicitPath reports a verify operand that is a filesystem path by form
// (slash, backslash, or drive prefix). A bare foo.service is a unit name
// even if that name exists in the current directory.
func isExplicitPath(arg string) bool {
	if strings.ContainsAny(arg, `/\`) {
		return true
	}
	return len(arg) >= 2 && arg[1] == ':'
}

func classifyVerifyOperands(operands []string, fileFlag bool) (paths, units []string, errMsg string) {
	if fileFlag {
		return operands, nil, ""
	}
	for _, a := range operands {
		if isExplicitPath(a) {
			paths = append(paths, a)
		} else {
			units = append(units, a)
		}
	}
	if len(paths) > 0 && len(units) > 0 {
		return nil, nil, "mixed unit names and file paths; use ./name, a drive path, or --file for paths, or pass only unit names"
	}
	return paths, units, ""
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
	name := ""
	if len(args) == 1 {
		name = unit.NormalizeName(args[0])
	}
	var st *protocol.StatusResult
	err := c.call(func(ctx context.Context, cl *protocol.Client) error {
		var err error
		st, err = cl.Status(ctx, name)
		return err
	})
	if err != nil {
		if protocolCode(err) == protocol.CodeNotFound {
			fmt.Fprintf(c.stderr, "winctl: %v\n", err)
			return 4
		}
		return c.rpcError(err)
	}
	code := c.printStatus(st)
	if code != 0 {
		return code
	}
	return statusExitCode(st)
}

func protocolCode(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) && pe != nil {
		return pe.Code
	}
	return ""
}

// statusExitCode is systemctl-shaped for a unit: 0 active, 3 loaded but
// not active (inactive, failed, activating, deactivating). Machine status
// is 0. Not-found is handled by the RPC error path (exit 4).
func statusExitCode(st *protocol.StatusResult) int {
	if st == nil || st.Unit == nil {
		return 0
	}
	if st.Unit.ActiveState == "active" {
		return 0
	}
	return 3
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
	la, errMsg := parseLogsArgs(args)
	if errMsg != "" {
		if errMsg == "help" {
			fmt.Fprint(c.stdout, usage)
			return 0
		}
		fmt.Fprintf(c.stderr, "winctl logs: %s\n", errMsg)
		return 2
	}
	cursor := ""
	printed := false
	for {
		var got *protocol.LogsResult
		err := c.call(func(ctx context.Context, cl *protocol.Client) error {
			var err error
			got, err = cl.Logs(ctx, protocol.LogsParams{
				Unit:   la.unit,
				Follow: la.follow,
				Since:  la.since,
				Cursor: cursor,
			})
			return err
		})
		if err != nil {
			return c.rpcError(err)
		}
		if len(got.Entries) > 0 {
			c.printLogsEntries(got)
			printed = true
		} else if !la.follow && !printed {
			return c.printLogs(got)
		}
		if got.Cursor != "" {
			cursor = got.Cursor
		}
		if got.More {
			continue
		}
		if !la.follow {
			return 0
		}
		if c.followStop != nil {
			select {
			case <-c.followStop:
				return 0
			default:
			}
		}
		if c.followStop != nil {
			select {
			case <-c.followStop:
				return 0
			case <-time.After(journalFollowPoll):
			}
		} else {
			time.Sleep(journalFollowPoll)
		}
	}
}

const journalFollowPoll = 200 * time.Millisecond

type logsArgs struct {
	unit   string
	follow bool
	since  string
}

func parseLogsArgs(args []string) (logsArgs, string) {
	var la logsArgs
	for i := 0; i < len(args); {
		a := args[i]
		switch {
		case a == "-h" || a == "-help" || a == "--help" || a == "help":
			return la, "help"
		case a == "--follow":
			la.follow = true
			i++
		case a == "--since":
			if i+1 >= len(args) {
				return la, "--since requires a value"
			}
			la.since = args[i+1]
			if strings.TrimSpace(la.since) == "" {
				return la, "--since requires a value"
			}
			i += 2
		case strings.HasPrefix(a, "--since="):
			la.since = strings.TrimPrefix(a, "--since=")
			if strings.TrimSpace(la.since) == "" {
				return la, "--since requires a value"
			}
			i++
		case strings.HasPrefix(a, "-"):
			return la, fmt.Sprintf("unknown flag %q", a)
		default:
			if la.unit != "" {
				return la, fmt.Sprintf("unexpected argument %q", a)
			}
			la.unit = unit.NormalizeName(a)
			i++
		}
	}
	if strings.TrimSpace(la.unit) == "" {
		return la, "unit name required"
	}
	return la, ""
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
	if r.OperationID != "" {
		fmt.Fprintf(c.stdout, "OperationID=%s\n", r.OperationID)
	}
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
		if m.Maintenance != nil {
			fmt.Fprintf(c.stdout, "  Maintenance: %s\n", m.Maintenance.State)
			if m.Maintenance.Error != "" {
				fmt.Fprintf(c.stdout, "  MaintenanceError: %s\n", m.Maintenance.Error)
			}
		}
		if m.ConfigRevision != "" {
			fmt.Fprintf(c.stdout, "ConfigRevision=%s\n", m.ConfigRevision)
		}
		fmt.Fprintf(c.stdout, "  Units: %d loaded\n", m.UnitsLoaded)
		fmt.Fprintf(c.stdout, "         %d active\n", m.UnitsActive)
		fmt.Fprintf(c.stdout, "         %d failed\n", m.UnitsFailed)
		fmt.Fprintf(c.stdout, "  Timers: %d loaded\n", m.TimersLoaded)
		if m.LogDroppedRecords != 0 || m.LogStorageErrors != 0 {
			fmt.Fprintf(c.stdout, "  Journal: %d dropped records, %d dropped bytes, %d storage errors (manager lifetime)\n", m.LogDroppedRecords, m.LogDroppedBytes, m.LogStorageErrors)
			if m.LogLastStorageError != "" {
				fmt.Fprintf(c.stdout, "  Journal error: %s\n", m.LogLastStorageError)
			}
		}
		if m.UserManagers > 0 || m.Lingering > 0 || m.LingerState != "" || m.UserNativeWork > 0 || len(m.UserRecovery) > 0 || len(m.UserInstances) > 0 {
			fmt.Fprintf(c.stdout, "  Users:  %d managers\n", m.UserManagers)
			fmt.Fprintf(c.stdout, "          %d lingering\n", m.Lingering)
			if m.LingerState != "" {
				fmt.Fprintf(c.stdout, "          linger records: %s", m.LingerState)
				if m.LingerError != "" {
					fmt.Fprintf(c.stdout, "; %s", m.LingerError)
				}
				fmt.Fprintln(c.stdout)
			}
			fmt.Fprintf(c.stdout, "          %d native operations pending\n", m.UserNativeWork)
			for _, user := range m.UserInstances {
				mode := user.Mode
				if mode == "" {
					mode = "unselected"
				}
				fmt.Fprintf(c.stdout, "          %s: %s; mode=%s; session=%d; pid=%d; interactive sessions=%d\n", user.SID, user.State, mode, user.SessionID, user.PID, user.InteractiveSessions)
			}
			for _, r := range m.UserRecovery {
				fmt.Fprintf(c.stdout, "          %s: %s", r.SID, r.State)
				if r.NextAttemptAt != "" {
					fmt.Fprintf(c.stdout, "; retry after %s", r.NextAttemptAt)
				}
				if r.Error != "" {
					fmt.Fprintf(c.stdout, "; %s", r.Error)
				}
				fmt.Fprintln(c.stdout)
			}
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
		if u.SubState != "" {
			fmt.Fprintf(c.stdout, "   Substate: %s\n", u.SubState)
		}
		if u.TerminationUncertain {
			if len(u.PendingCleanup) != 0 {
				fmt.Fprintf(c.stdout, "PendingCleanup=%s\n", strings.Join(u.PendingCleanup, ","))
			}
			fmt.Fprintln(c.stdout, "Termination: unconfirmed; retry stop")
		}
		if u.Reason != "" {
			fmt.Fprintf(c.stdout, "     Reason: %s\n", u.Reason)
		}
		if u.MainPID != 0 {
			fmt.Fprintf(c.stdout, "   Main PID: %d\n", u.MainPID)
		}
		if u.InvocationID != "" {
			fmt.Fprintf(c.stdout, "InvocationID=%s\n", u.InvocationID)
		}
		if u.ConfigRevision != "" {
			fmt.Fprintf(c.stdout, "ConfigRevision=%s\n", u.ConfigRevision)
		}
		if u.LastOperationID != "" {
			fmt.Fprintf(c.stdout, "LastOperationID=%s\n", u.LastOperationID)
		}
		if u.InvocationConfigRevision != "" {
			fmt.Fprintf(c.stdout, "InvocationConfigRevision=%s\n", u.InvocationConfigRevision)
		}
		if u.ArmedConfigRevision != "" {
			fmt.Fprintf(c.stdout, "ArmedConfigRevision=%s\n", u.ArmedConfigRevision)
		}
		if u.TimerStorageState != "" {
			if u.TimerScheduleState != "" {
				fmt.Fprintf(c.stdout, "TimerSchedule=%s\n", u.TimerScheduleState)
			}
			fmt.Fprintf(c.stdout, "TimerStorage=%s", u.TimerStorageState)
			if u.TimerStorageError != "" {
				fmt.Fprintf(c.stdout, "; %s", u.TimerStorageError)
			}
			fmt.Fprintln(c.stdout)
		}
		if a := u.TimerActivation; a != nil {
			fmt.Fprintf(c.stdout, "TimerActivation=%s; result=%s; target=%s; scheduled=%s; actual=%s\n", a.ID, a.Result, a.Unit, a.Scheduled, a.Actual)
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
		if u.CPUWeight != 0 {
			fmt.Fprintf(c.stdout, "  CPUWeight: %d\n", u.CPUWeight)
		}
		if u.CPUQuota != 0 {
			fmt.Fprintf(c.stdout, "   CPUQuota: %d%%\n", u.CPUQuota)
		}
		if u.IoPriority != "" {
			fmt.Fprintf(c.stdout, " IoPriority: %s\n", u.IoPriority)
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
		if u.ScheduleState == "planning" {
			fmt.Fprintln(c.stdout, "  schedule: planning")
		}
		if u.StorageState == "loading" || u.StorageState == "failed" {
			fmt.Fprintf(c.stdout, "  storage: %s", u.StorageState)
			if u.StorageError != "" {
				fmt.Fprintf(c.stdout, "; %s", u.StorageError)
			}
			fmt.Fprintln(c.stdout)
		}
		if a := u.Activation; a != nil {
			fmt.Fprintf(c.stdout, "  activation: %s; %s\n", a.ID, a.Result)
		}
	}
	return 0
}

func (c *cli) printLogs(got *protocol.LogsResult) int {
	if len(got.Entries) == 0 {
		fmt.Fprintf(c.stdout, "No journal entries for %s.\n", got.Unit)
		return 0
	}
	c.printLogsEntries(got)
	return 0
}

func (c *cli) printLogsEntries(got *protocol.LogsResult) {
	for _, e := range got.Entries {
		fmt.Fprintln(c.stdout, formatLogEntry(e))
	}
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
