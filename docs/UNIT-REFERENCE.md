# Unit reference for the public beta

This documents the implemented format. It takes precedence over proposed syntax
in Design v2. Existing core directives and argument parsing are compatibility
commitments across the beta series. Files currently have no version marker;
future incompatible semantics require an explicit migration boundary.

## File grammar

Use UTF-8, optionally with a BOM, and `.service`, `.target`, `.timer`, `.path`,
`.registry`, or `.eventlog` filenames. Unit names/references are normalized to
lower case. Section and directive names are case-sensitive.

Blank lines and whole-line `#` or `;` comments are ignored. Inline comments are
literal value text. Values are trimmed around `=`. A trailing backslash preceded
by whitespace joins the next line; a Windows path ending in backslash does not.
Repeated sections are processed in file order. Scalars use the last assignment;
the list exceptions are specified below. Unknown sections/directives and sections
in the wrong file kind are errors. `[Unit]` and `[Install]` apply to every kind;
the kind-specific section applies only to its corresponding file suffix.

Booleans accept yes/no, true/false, on/off, 1/0, y/n, and t/f, case-insensitively.
Durations accept nonnegative seconds without a suffix, `5s`, `250ms`, `1h 30min`,
and `infinity`. Use explicit finite timeout values for applications.

There is no implicit shell, executable search, environment expansion, template
specifier expansion, or systemd drop-in syntax. Invoke PowerShell or cmd.exe
explicitly if the application needs a shell.

## Core service example

Save as `app.service`, adjust paths, verify, then reload and start it:

```ini
[Unit]
Description=Example application
StartLimitIntervalSec=10s
StartLimitBurst=5

[Service]
Type=simple
ExecStart=["C:\\Apps\\Example\\app.exe", "--config", "C:\\Apps\\Example\\app.json"]
WorkingDirectory=C:\Apps\Example
Environment=LOG_LEVEL=info "APP_LABEL=Example application"
Restart=on-failure
RestartSec=2s
TimeoutStopSec=10s

[Install]
WantedBy=default.target
```

```powershell
winctl verify --file .\app.service
# Copy to C:\ProgramData\winunitd\units\app.service as administrator.
winctl daemon-reload
winctl enable app.service
winctl start app.service
winctl status app.service
winctl logs app.service
```

System units run as LocalSystem. A unit file does not select another account.
Use the separately documented user-manager path for user identity; do not place
user-writable executables or scripts in a privileged system unit.

## Arguments and environment

`ExecStart=` accepts a JSON string array (recommended for exact arguments), or a
command line split on unquoted whitespace. In the command-line form, double
quotes group text and are removed; backslashes are literal, single quotes do not
group text, and shell operators are ordinary arguments. JSON uses normal JSON
escaping, including doubled Windows backslashes. Every array element must be a
string; an empty argument is allowed, an empty executable is not.

Repeated `ExecStartArg=` appends one literal trimmed argument per line, including
an empty argument for an empty value. Quotes on those lines are literal. When
used with a non-JSON `ExecStart`, that value is the entire unquoted executable
path, with no inline arguments. A path containing spaces must exist when verified
in this form; use JSON to avoid that filesystem-dependent ambiguity. JSON argv
may also be followed by `ExecStartArg` entries. Repeating `ExecStart` replaces its
base value but does not clear the accumulated extra arguments.

`ExecStart` must name an absolute Windows executable. `WorkingDirectory` must be
absolute if provided; set it explicitly. An omitted directory produces a warning
and must not be used to assume a particular inherited directory.

Repeated `Environment=` appends assignments; an empty value clears the accumulated
list. Each line contains whitespace-separated `NAME=value` assignments, with
single or double quotes available for grouping. Values are literal: `${NAME}`
and `%NAME%` are not substituted.

## Core directives

| Section / directive | Behavior and default |
| --- | --- |
| Unit: `Description` | Display text; empty by default |
| Unit: `Requires`, `Wants` | Pull dependencies into the start plan; Requires propagates start failure, Wants permits failure |
| Unit: `After`, `Before` | Ordering only; does not pull in the named unit |
| Unit: `PartOf` | Reverse stop/restart participation; restart restores active/activating members, leaving otherwise unselected idle members inactive |
| Unit: `StartLimitIntervalSec`, `StartLimitBurst` | Defaults 10s / 5; burst 0 disables the limit; explicit start resets the budget |
| Service: `Type` | Default simple; simple launches a process; notify waits for readiness; oneshot waits for exit; proxies described below |
| Service: `RemainAfterExit` | oneshot only; no (default) finishes inactive after success; yes retains active state |
| Service: `ExecStart`, `ExecStartArg`, `WorkingDirectory`, `Environment` | Arguments/environment rules above |
| Service: `Restart` | no (default), on-failure, always, on-watchdog |
| Service: `RestartSec` | Recovery delay; default 100ms; setting 2s is a practical application default |
| Service: `TimeoutStartSec` | Startup/oneshot/readiness timeout; set explicitly when startup can wait |
| Service: `TimeoutStopSec` | Cleanup wait; default 5s; does not make stop graceful |
| Install: `WantedBy` | Enable links for named targets; enable does not itself start a service |

Dependency lists and `WantedBy` split on whitespace, append on repetition, and
clear on an empty assignment. Use `.target` files with `[Unit]`/`[Install]` to
group services. `Requires` and ordering are independent: specify `After` when a
dependent must wait for required startup. A failed transaction does not roll back
every member that already started.

Stop currently terminates the owned process job. No `ExecStop` or graceful signal
is implemented. A successful oneshot finishes inactive by default, after output
drain and owned process cleanup. Its start operation succeeds even though the
unit is inactive, so ordered dependents can proceed. A subsequent start runs it
again; concurrent compatible explicit starts share one operation rather than
queueing another run. `RemainAfterExit=yes` keeps the completed unit active;
start is then a no-op, and stop followed by start (or restart) runs it again.
Reload does not change the completion policy of an existing invocation.

This changes the pre-feature beta default directly: omitted `RemainAfterExit`
now means `no`. Use `yes` when completed work should remain active. There is no
format-version switch or automatic unit rewrite. Other service types reject this
directive. Existing `Restart=always` behavior for oneshots remains supported;
use `Restart=no` for on-demand maintenance that must not retry automatically.

## Additional accepted directives and experimental capabilities

The syntax below is accepted and retained. Its broader runtime qualification is
outside the core beta guarantee; passing `verify` is not a qualification claim.

| Section / directives | Current behavior / limitation |
| --- | --- |
| Unit: `BindsTo` | Dependency/stop-plan participation exists; unexpected-disappearance propagation is not a supported beta guarantee |
| Unit: `RequiresInteractiveSession` | Default no; skip without a suitable session; user/session modes remain experimental until focused qualification |
| Service: `NotifyAccess` | main only; default main |
| Service: `WatchdogSec`, `WatchdogMode` | Positive duration enables watchdog; mode notify (default), tcp, or http |
| Service: `WatchdogEndpoint`, `WatchdogExpectedStatus` | TCP/HTTP loopback endpoint only; HTTP expected status defaults to 200 |
| Service: `MemoryMax`, `ProcessLimit` | Job-wide memory commit cap (K/M/G sizes) and positive process count |
| Service: `PriorityClass` | idle, below-normal, normal, above-normal, high; realtime rejected |
| Service: `CPUWeight`, `CPUQuota` | Weight 1-10000 maps to Windows 1-9; quota 1%-100% is total-machine CPU; mutually exclusive |
| Service: `IoPriority` | idle, low, normal, high; failure applying it fails activation |
| Service: `ServiceName` | Required by Type=scm; proxy for an existing system service, not its installation |
| Service: `TaskName` | Required by Type=scheduled-task; proxy for an existing task, not its definition |
| Timer: `OnBootSec`, `OnStartupSec`, `OnUnitActiveSec` | Duration triggers; omitted triggers disabled |
| Timer: `OnCalendar` | Repeatable calendar expressions, e.g. daily, Mon..Fri 03:00, or *-*-* 12:30:00 |
| Timer: `Persistent` | Default no; persisted scheduling is experimental and not crash-safe delivery |
| Timer: `Unit` | Activated unit; defaults to same basename .service |
| Path: `PathChanged`, `PathExists` | Repeatable absolute paths; Changed is OR, Exists is AND; mixed modes can each activate |
| Registry: `RegistryChanged` | Repeatable registry key/subtree triggers; HKLM for system scope, HKCU also available to user scope |
| EventLog: `EventLogTrigger` | Repeatable Channel:EventID=number; user scope rejects System/Security |

Timers require at least one trigger. Path/registry/eventlog units always activate
the neighboring same-basename `.service`; they do not accept `Unit=`. Empty
trigger assignments are not list-reset syntax. Paths are non-recursive watches.
Do not rely on persistent timers for lossless or exactly-once job delivery.

Proxies are system-manager only, do not own native process trees, and do not
continuously observe the native service/task after startup. Some process-only
directives on proxies produce ignored-setting warnings; read verifier warnings.
See [runtime details](../README.md#proxy-unit-types) for those restrictions.

Notify clients must consume the `WINUNITD-NOTIFY/1` acceptance banner before
sending. Upgrade the daemon and notify helper together. Readiness and heartbeat
details are in [the notify documentation](../README.md#notify-and-watchdog).

## Unsupported syntax and updates

`ExecStop`, `User`, `Group`, `EnvironmentFile`, `SessionMode`,
`SessionPolicy`, `RestartMaxDelaySec`, `RestartBackoff`, and unknown directives
are rejected. Do not copy a Linux systemd unit without checking this reference.

Use `winctl verify` before deployment and `daemon-reload` after changing files.
Reload accepts the whole configuration/graph or retains the previous revision.
It does not restart live processes; restart explicitly to apply changed launch
settings. Removed live units remain available for status/log/stop. Operation IDs
and their retention limits are documented in [operation history](OPERATIONS.md).
