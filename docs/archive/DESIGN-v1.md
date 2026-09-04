# winunitd / winctl — Design Notes

Archived September 5, 2026. Superseded by [Design v2](../../DESIGN.md). This document preserves the original section numbers referenced by existing source comments and tests; those references describe historical implementation contracts, not the current target design. Original text follows unchanged.

## 1. Purpose

`winunitd` is a Windows-native service and process supervision layer inspired by the useful operational model of `systemd`, without attempting to reproduce Linux internals that do not map cleanly onto Windows.

The project would provide:

- Declarative unit files
- Dependency-aware startup and shutdown
- Process supervision and restart policies
- Reliable scheduled/timer execution
- Watchdog and health monitoring
- Machine-level and per-user managers
- User-level lingering
- Unified lifecycle control through `winctl`
- Structured status and logging
- Native Windows integration using SCM, Job Objects, access tokens, named pipes, Event Log/ETW, and Windows session APIs

The central architectural decision is:

> Only `winunitd` itself should need to be a Windows Service. Managed workloads should normally be ordinary processes owned and supervised by `winunitd`, rather than individually registered with the Windows Service Control Manager.

That avoids reproducing SCM's operational limitations and gives `winunitd` control over dependency ordering, process trees, restart logic, timers, watchdogs, user managers, and logging.

---

## 2. Design Goals

### 2.1 Primary goals

1. **Declarative configuration**
   - Service behavior should be described in text files.
   - Configuration should be versionable and machine-readable.
   - Operators should not need to manually construct Task Scheduler entries or SCM services.

2. **Predictable lifecycle semantics**
   - `start`, `stop`, `restart`, `reload`, `enable`, `disable`, `mask`.
   - Dependency-aware ordering.
   - Explicit restart behavior.
   - Clear timeout semantics.

3. **Strong process ownership**
   - A unit owns its entire process tree.
   - Child processes cannot silently escape supervision.
   - Stopping a unit reliably tears down the full process tree unless explicitly configured otherwise.

4. **Machine and user scopes**
   - `winctl ...`
   - `winctl --user ...`
   - Persistent user managers independent of interactive login sessions.

5. **Lingering**
   - A user's runtime can remain active after logout.
   - A lingering user manager can start at boot before the user logs in.

6. **Integrated timers**
   - Calendar timers
   - Monotonic timers
   - Repeating timers
   - Persistent/catch-up behavior

7. **Integrated health supervision**
   - Process exit monitoring
   - Watchdog heartbeats
   - TCP/HTTP/pipe probes
   - Startup readiness checks
   - Optional freeze/hang detection

8. **Useful diagnostics**
   - Structured status
   - Recent logs
   - Exit code history
   - Restart counters
   - Dependency failure explanation

9. **Native Windows implementation**
   - No WSL requirement.
   - No POSIX compatibility dependency.
   - No dependence on Task Scheduler for core semantics.

---

## 3. Non-Goals

`winunitd` should not try to become a complete Windows replacement shell, init system, or configuration management platform.

Not initially:

- Device management
- Kernel driver loading
- Windows Update management
- Registry policy management
- Package management
- Full Windows session replacement
- Mandatory conversion of existing Windows Services
- Full compatibility with every `systemd` directive

Compatibility with familiar `systemd` concepts is useful, but semantic correctness on Windows is more important than syntax compatibility.

---

## 4. Core Architecture

```text
                   +----------------------+
                   |      winctl.exe      |
                   +----------+-----------+
                              |
                       Named Pipe / RPC
                              |
                   +----------v-----------+
                   |     winunitd.exe     |
                   |   system manager     |
                   +----------+-----------+
                              |
        +---------------------+----------------------+
        |                     |                      |
        v                     v                      v
  Unit Manager          Timer Engine          User Manager Host
        |                                            |
        v                                            v
 Dependency Graph                            per-user managers
        |                                            |
        v                                            v
 Process Supervisor                         user-scoped units
        |
        v
 Windows Job Objects
        |
        v
 managed process trees
```

The machine-wide `winunitd` process runs as a conventional Windows Service, likely under `LocalSystem`.

Its responsibilities:

- Parse system unit files
- Maintain the dependency graph
- Start and stop system units
- Own system-scoped Job Objects
- Host the timer scheduler
- Launch and supervise persistent user managers
- Broker privileged operations
- Expose the control API
- Maintain runtime state and logs

---

## 5. Process Supervision

### 5.1 Job Objects

Every managed service unit should normally receive its own Windows Job Object.

Benefits:

- Track the entire process hierarchy
- Terminate all descendants on stop
- Apply job-wide MemoryMax=, ProcessLimit=, and PriorityClass= (R1)
- Apply CPUWeight= / CPUQuota= (Job Object CPU rate) and IoPriority= (R2)
- Detect process lifecycle events
- Prevent orphaned helper processes

Default policy:

```ini
[Service]
KillMode=job
```

Possible alternatives:

```ini
KillMode=process
KillMode=none
```

But `job` should be the safe default.

### 5.2 Breakaway handling

Windows allows processes to escape a Job under some circumstances.

`winunitd` should:

- Disallow breakaway by default
- Detect when a child escapes if possible
- Provide an explicit override for software that legitimately requires breakaway

Example:

```ini
AllowJobBreakaway=no
```

### 5.3 Console control

Shutdown should support configurable escalation:

1. Application-specific stop action
2. CTRL_BREAK_EVENT / console signal where applicable
3. WM_CLOSE or custom IPC hook if configured
4. TerminateProcess
5. Job termination

Example:

```ini
StopSignal=ctrl-break
TimeoutStopSec=30s
KillAfterTimeout=yes
```

---

## 6. Unit Types

Initial unit types should be deliberately small.

### 6.1 `.service`

Long-running process or one-shot command.

### 6.2 `.timer`

Scheduled activation.

### 6.3 `.target`

Logical grouping and synchronization point.

### 6.4 `.path` ★

Windows-native companion unit. `foo.path` activates `foo.service` by basename (same lock as timers, `.registry`, and `.eventlog`; no `Unit=`). While the path unit is active it is watching. File and directory watches only (`ReadDirectoryChangesW`). Do not overload `.path` for registry; that is `.registry`.

```ini
[Path]
PathChanged=C:\Data\incoming
PathExists=C:\Data\incoming\ready.flag
```

`PathChanged=` is repeatable (watch each path; any change fires — OR). `PathExists=` is repeatable and is **AND**: the counterpart starts only when every `PathExists=` path exists. This is a lock versus systemd, where repeatable `PathExists=` is OR. Absolute Windows paths only (drive-letter `C:\…` or UNC `\\server\share\…`). A `PathChanged=` file path watches the parent directory and filters by name. A `PathChanged=` directory path watches that directory. Watches are **non-recursive** (this directory only; subdirectory changes do not fire). `PathExistsIsDirectory=` and directory-empty triggers are not implemented.

On activate, if every `PathExists=` path already exists, start the counterpart once (a still-running `Type=simple` is not restarted). While the path unit is active, watch so a later creation can satisfy AND and start; deleting a `PathExists=` path does not stop a running counterpart. Mixing `PathChanged=` and `PathExists=` on the same unit: **either** may start the counterpart (any `PathChanged=` fire, or the `PathExists=` AND becoming satisfied).

The system manager and user managers both accept `.path` units. A missing or unwatchable `PathChanged=` path at activate fails the path unit with reason `configuration`; the daemon stays up. A missing `PathExists=` path does not fail the unit (it waits for creation). An unwatchable `PathExists=` path (no existing ancestor directory to watch) fails with reason `configuration`.

A change or a satisfied `PathExists=` **starts** `foo.service` if it is inactive or failed. If the service is already `active`, do not restart it. A oneshot that exits is the repeatable pattern.

Enable with `WantedBy=` like other units (usually `default.target`). There is no `paths.target`. `winctl list-units` shows `.path` units. `verify` on the pair fails for a missing `foo.service`, a non-absolute path, or an empty path. A path unit must specify `PathChanged=` or `PathExists=` (or both).

### 6.5 `.socket`

Potential later addition.

This is less straightforward than on Unix because transparent socket activation semantics differ on Windows.

Could initially support:

- Named pipe activation
- TCP listener proxy activation

But should not be required for MVP.

### 6.6 `.registry` ★

Windows-native companion unit. `foo.registry` activates `foo.service` by basename (same lock as timers; no `Unit=`). While the registry unit is active it is watching.

```ini
[Registry]
RegistryChanged=HKLM\Software\Example
```

`RegistryChanged=` is repeatable (watch each key). Hive syntax is `HKLM\…` or `HKCU\…` only: no PowerShell drive (`HKLM:\`), no implicit PowerShell. Watch the key and its subtree (`RegNotifyChangeKeyValue`).

The system manager accepts `HKLM` only (`HKCU` there is LocalSystem’s hive; `verify` fails). A user manager accepts `HKCU` (that user) and `HKLM`. A missing key at activate fails the registry unit with reason `configuration`; the daemon stays up.

A change **starts** `foo.service` if it is inactive or failed. If the service is already `active`, do not restart it. A oneshot that exits is the repeatable pattern.

Enable with `WantedBy=` like other units (usually `default.target`). There is no `registries.target`. `winctl list-units` shows `.registry` units. `verify` on the pair fails for a missing `foo.service`, a bad hive, or an empty path.

### 6.7 `.eventlog` ★

Windows-native companion unit. `foo.eventlog` activates `foo.service` by basename (same lock as timers and `.registry`; no `Unit=`). While the event log unit is active it is watching.

```ini
[EventLog]
EventLogTrigger=System:EventID=1234
```

`EventLogTrigger=` is repeatable (any match fires). Grammar is `<Channel>:EventID=<uint16>` only: Channel is a literal log name (`System`, `Application`, a custom log). No XPath, no `Provider=`, no `Level=` in the unit file. Subscribe with `EvtSubscribe` (push). The subscribe query is `*[System[(EventID=N)]]` derived from `EventLogTrigger=` (not a unit-file query language). A null or empty query is a subscribe error; it does not match every event on the channel. EventID filtering is done by `EvtSubscribe`; the callback does not `EvtRender` every candidate to XML.

The system manager accepts any readable channel (LocalSystem). A user manager `verify` allows `Application` and custom names; `System` / `Security` on `--user` is a verify error. Activate still fails closed if `EvtSubscribe` denies.

Subscribe failure or an unknown channel at activate fails the event log unit with reason `configuration`; the daemon stays up.

A matching event **starts** `foo.service` if it is inactive or failed. If the service is already `active`, do not restart it. A oneshot that exits is the repeatable pattern.

Enable with `WantedBy=` like other units (usually `default.target`). There is no `eventlogs.target`. `winctl list-units` shows `.eventlog` units. `verify` on the pair fails for a missing `foo.service`, bad grammar, `EventID=0` or non-numeric, or an empty channel.

---

## 7. Unit File Locations

Suggested paths:

### System units

```text
C:\ProgramData\winunitd\units\
```

### User units

```text
%LOCALAPPDATA%\winunitd\units\
```

Potential administrative override layer:

```text
C:\ProgramData\winunitd\units.d\
```

Potential built-in/vendor units:

```text
C:\Program Files\winunitd\units\
```

Load precedence could follow:

```text
user overrides
    >
administrator overrides
    >
vendor units
```

---

## 8. Example Service Unit

```ini
[Unit]
Description=Hermes Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=C:\Tools\Hermes\hermes.exe agent
WorkingDirectory=C:\Tools\Hermes
Restart=on-failure
RestartSec=5s
TimeoutStartSec=30s
TimeoutStopSec=20s

Environment=HERMES_PROFILE=default
Environment=LOG_LEVEL=info

WatchdogSec=60s
WatchdogMode=pipe

[Install]
WantedBy=default.target
```

---

## 9. Service Types

Suggested service types:

### `Type=simple`

Process is considered started immediately after successful process creation.

### `Type=exec`

Started once `CreateProcess` succeeds and the executable image is initialized.

### `Type=notify`

Application explicitly signals readiness to `winunitd`.

Possible mechanism:

- Named pipe
- Local RPC
- Shared memory event

Example:

```ini
Type=notify
NotifyAccess=main
```

### `Type=oneshot`

Run command to completion.

```ini
Type=oneshot
RemainAfterExit=yes
```

Useful for:

- initialization
- mount preparation
- configuration generation
- firewall changes
- script execution

### `Type=scm`

Orchestrates an existing named SCM service (see §51). `winctl start` calls StartService; `winctl stop` calls StopService; status is QueryServiceStatusEx mapped to ActiveState. This does not register, change, or delete SCM configuration.

### `Type=scheduled-task`

Orchestrates an existing registered Task Scheduler task (see §52). `winctl start` calls IRegisteredTask.Run; `winctl stop` ends running instance(s); status is task/instance state mapped to ActiveState. This does not create, edit, delete, or enable/disable the task definition.

### `Type=forking`

Probably omit.

There is little reason to encourage daemonization behavior on Windows.

---

## 10. Dependency Model

Support familiar concepts:

```ini
Requires=
Wants=
After=
Before=
Conflicts=
PartOf=
BindsTo=
```

Semantics must remain separate:

- `Requires=` defines requirement
- `After=` defines ordering

This distinction is one of systemd's best design choices and should be preserved. It applies to stop as well as start:

- Stopping a unit does not stop its forward `Requires=` (or `Wants=`): units it lists stay up.
- `After=` / `Before=` never propagate a single-unit stop. They only order units already in the transaction.

Example:

```ini
[Unit]
Requires=postgres.service
After=postgres.service
```

### Failure propagation

If `postgres.service` cannot start:

- units with `Requires=postgres.service` should fail
- units with `Wants=postgres.service` may continue

---

## 11. Targets

Targets provide logical grouping.

Built-in examples:

```text
basic.target
network.target
network-online.target
default.target
shutdown.target
```

User scope:

```text
user.target
default.target
graphical-session.target
```

Windows-native targets could also exist:

```text
interactive-session.target
rdp-session.target
network-domain.target
network-private.target
```

Care should be taken not to overfit to current Windows behavior.

---

## 12. Enable / Disable Model

`enable` should not mean "start now."

It should only establish startup relationships.

Example:

```powershell
winctl enable hermes.service
```

Equivalent operation:

```text
default.target wants hermes.service
```

Potential implementation:

```text
C:\ProgramData\winunitd\enabled\default.target\hermes.service
```

Could use:

- tiny link files
- JSON metadata
- NTFS symlinks

Avoid requiring NTFS symlink privileges if possible.

---

## 13. CLI Design

Executable:

```text
winctl.exe
```

Core commands:

```powershell
winctl status
winctl status foo.service

winctl start foo
winctl stop foo
winctl restart foo
winctl reload foo

winctl enable foo
winctl disable foo
winctl mask foo
winctl unmask foo

winctl list-units
winctl list-unit-files
winctl list-dependencies foo
winctl list-timers

winctl show foo
winctl cat foo
winctl edit foo

winctl daemon-reload
```

User scope:

```powershell
winctl --user status
winctl status --user
winctl --user enable hermes.service
winctl --user start hermes.service
```

`--user` is accepted before or after the verb. Both talk to the per-user pipe.

`winctl status UNIT` exit codes are systemctl-shaped: 0 active, 3 inactive or failed (loaded but not active), 4 not loaded. Transport/protocol errors keep their existing non-zero exit.

Machine/user ambiguity should be explicit.

---

## 14. User Managers

This is one of the most important features.

Each user should be able to have a dedicated manager runtime.

Conceptually:

```text
winunitd.exe
    |
    +-- user-manager.exe --sid S-1-5-21-...
    |       |
    |       +-- hermes.service
    |       +-- syncthing.service
    |       +-- backup.timer
    |
    +-- user-manager.exe --sid S-1-5-21-...
```

Potentially the same executable:

```text
winunitd.exe --user-manager <SID>
```

### Runtime identity

The user manager must execute units under the user's security context.

Possible mechanisms:

- Stored service credentials
- S4U logon
- Token captured from interactive login
- Virtual account
- gMSA for domain/service scenarios

No single method fits every case.

The architecture should therefore separate:

```text
user identity
```

from:

```text
interactive session
```

This is crucial for lingering.

### Linger token (no session)

At boot, a lingering user manager is launched with no interactive session. Token acquisition:

1. **S4U over a trusted LSA connection.** winunitd runs as LocalSystem and therefore holds `SeTcbPrivilege`. It calls `LsaRegisterLogonProcess` (not `LsaConnectUntrusted`). An untrusted connection yields an identification-level token; `DuplicateTokenEx` to `SecurityImpersonation` / `TokenPrimary` then fails with `ERROR_BAD_IMPERSONATION_LEVEL`, so `CreateProcessAsUser` never gets a usable primary token.

2. **Optional named CredMan/LSA URI** on the linger record only. The URI is a store name (`credman://…` or `lsa://…`), never a password. Passwords must not appear in unit files, environment, or files.

3. **Path selection.** S4U always runs first. The store URI is tried only when it is present **and** the S4U token is insufficient for outbound network credentials. Sufficiency is a real logon-session probe (`TokenStatistics` + `LsaGetLogonSessionData` `LogonType`), not a hardcoded “S4U never has network creds”. Network logons do not cache outbound creds; Batch / Interactive / Service / NetworkCleartext / NewCredentials and the interactive variants do. If the URI logon fails, the S4U token is kept. The daemon logs which path produced the token (`s4u` vs `store-uri`).

4. **URI fallback logon type.** `LogonUserW` uses `LOGON32_LOGON_BATCH` (4), not `LOGON32_LOGON_NETWORK` (3). Network logons do not cache credentials for outbound SSO; Batch does, which is the reason the URI exists. The password is held as `[]uint16` through `LogonUserW` and zeroed after use. CredMan generic blobs written by `cmdkey` / PowerShell are UTF-16LE (`CRED_TYPE_GENERIC`, then `CRED_TYPE_DOMAIN_PASSWORD`).

---

## 15. Lingering

Desired UX:

```powershell
winctl enable-linger alice
winctl disable-linger alice
```

Behavior:

When lingering is enabled:

- user manager starts during system boot
- it remains active when no interactive session exists
- enabled user services may continue after logout
- enabled timers continue firing
- login is not required to establish the runtime

When lingering is disabled:

- user manager starts on first login
- user manager may stop after the last interactive session disappears

### Important Windows complication

A logged-out user does not automatically have the same token/resource environment as an interactive session.

Therefore units may need explicit capability declarations.

Example:

```ini
RequiresInteractiveSession=yes
```

Or:

```ini
SessionMode=linger
SessionMode=interactive
SessionMode=either
```

Potential behavior:

- `linger`: may run without desktop/session
- `interactive`: only run while user has a suitable interactive session
- `either`: prefer interactive token, fall back to linger token

---

## 16. Session-Aware User Units

Windows may have:

- console session
- RDP sessions
- multiple concurrent sessions
- disconnected but still active sessions

Therefore a useful extension may be:

```ini
SessionPolicy=any
SessionPolicy=console
SessionPolicy=rdp
SessionPolicy=active
SessionPolicy=none
```

Possible units:

```text
graphical-session.target
console-session.target
rdp-session.target
```

This would allow:

```ini
[Unit]
After=graphical-session.target
PartOf=graphical-session.target
```

for GUI utilities.

---

## 17. Timers

Timers should be fully internal to `winunitd`.

Do not proxy Task Scheduler unless explicitly requested.

### Calendar timer

```ini
[Timer]
OnCalendar=Mon..Fri 03:00
Persistent=yes
RandomizedDelaySec=10m

[Install]
WantedBy=timers.target
```

### Boot-relative timer

```ini
[Timer]
OnBootSec=5m
OnUnitActiveSec=1h
```

### Startup-relative timer

```ini
OnStartupSec=30s
```

### User-login timer

Potential Windows extension:

```ini
OnUserLoginSec=10m
```

### Persistent timer semantics

If:

```ini
Persistent=yes
```

and the machine was off during the scheduled time, the job should run shortly after startup.

State should record:

```text
last scheduled execution
last actual execution
last successful execution
```

---

## 18. Scheduler Design

Use:

- monotonic clock for relative timers
- wall clock for calendar timers

The scheduler must correctly handle:

- sleep
- hibernation
- DST transitions
- manual clock changes
- timezone changes
- missed executions
- repeated/ambiguous wall-clock times

Avoid naïvely relying on `Sleep()` until the next timer.

Use a scheduler heap/priority queue and recalculate calendar deadlines after relevant clock-change notifications.

### Calendar Next / Previous (civil time)

`OnCalendar=` matching uses the zone of the reference instant (`from` / `before`). Candidates are civil dates, not `time.Date` overflow:

- Reject a candidate whose year, month, or day after construction is not the intended civil date. `*-*-31` matches 31 January, 31 March, … and never 3 March (February 31 does not exist). The same rule applies to `*-2-29` in a non-leap year (skip to the next leap-year 29 February).
- Iterate chronologically from `max(from, earliest civil date allowed by fixed year/month/day fields)` (and the symmetric latest-date bound for Previous). Do not substitute fixed fields into “today + i”: `2027-*-*` from 2026-09-01 is 2027-01-01, not 2027-09-01; `*-12-*` from January is 1 December, not 15 December.

DST, pinned (not left to `time.Date`, which disagrees across zones):

- **Spring-forward gap** (the specified wall time does not exist that day): fire at the **first valid instant after the gap**. Example: `*-*-* 02:30` on the US spring-forward Sunday fires at 03:00 local, not the next day and not 03:30.
- **Fall-back overlap** (the specified wall time occurs twice): fire at the **first occurrence**, once. Next after that instant is the next matching civil day, not the second copy the same morning.

---

## 19. Watchdog Architecture

Support multiple watchdog modes.

### 19.1 Native heartbeat

Preferred mode:

```ini
WatchdogMode=notify
WatchdogSec=30s
```

Application sends heartbeat to supervisor.

Could use a local named pipe:

```text
\\.\pipe\winunitd\notify\<unit-id>
```

Protocol could support:

```text
READY=1
STATUS=Processing queue
WATCHDOG=1
MAINPID=1234
```

This intentionally resembles `sd_notify`.

### 19.2 TCP

```ini
WatchdogMode=tcp
WatchdogEndpoint=127.0.0.1:8080
WatchdogSec=30s
```

### 19.3 HTTP

```ini
WatchdogMode=http
WatchdogEndpoint=http://127.0.0.1:8080/health
WatchdogExpectedStatus=200
```

### 19.4 Process responsiveness

Possible later mode:

```ini
WatchdogMode=window
```

Detect hung GUI processes using Windows message responsiveness.

This should not be the default because Windows "Not Responding" semantics are imperfect.

---

## 20. Restart Policies

Support:

```ini
Restart=no
Restart=always
Restart=on-success
Restart=on-failure
Restart=on-abnormal
Restart=on-watchdog
```

Shipped restart policies are `no`, `always`, `on-failure`, and `on-watchdog`.
`on-success` and `on-abnormal` are not parsed.

Additional parameters:

```ini
RestartSec=5s
```

`RestartMaxDelaySec=` and `RestartBackoff=` are not parsed (unknown
directive; same verify policy as other deferred keys).

Rate limiting is `[Unit]`:

```ini
StartLimitIntervalSec=10s
StartLimitBurst=5
```

Omitted values default to Interval=10s and Burst=5 (systemd-shaped).
`StartLimitBurst=0` disables the limit (unlimited). A non-positive
`StartLimitIntervalSec=` also disables it.

Each unit start (including a `Restart=` relaunch) records a timestamp on
the per-unit runtime. When `StartLimitBurst` starts fall inside
`StartLimitIntervalSec`, the manager does not schedule another restart:
the unit becomes `failed` with reason `start-limit` (`winctl status`,
§44).

An explicit `winctl start` (manager Start) on a start-limit-hit unit
resets the timestamp list and may launch again. The limit can then hit
again. `RestartSec` is unchanged.

After burst limit is exceeded:

```text
failed (start-limit)
```

---

## 21. Readiness and Health

Separate concepts:

```text
process exists
ready
healthy
```

A service can therefore be:

```text
activating
active (ready)
active (degraded)
failed
```

Potential status:

```text
Hermes Agent
  Loaded: loaded
  Active: active (running)
   Ready: yes
  Health: healthy
 Main PID: 4216
      Job: 0x0000028...
    Since: 2026-08-31 10:24:11
 Restarts: 1
 Watchdog: notify, 60s
```

---

## 22. Logging

### Goals

Every managed process should receive predictable stdout/stderr handling.

Default:

```ini
StandardOutput=journal
StandardError=journal
```

Alternative targets:

```ini
StandardOutput=inherit
StandardOutput=file:C:\Logs\foo.log
StandardOutput=null
```

### Log storage

System journal root:

```text
C:\ProgramData\winunitd\journal\
```

User managers use `%LOCALAPPDATA%\winunitd\journal\`. Tests inject a temp `BaseDir`.

On-disk format is JSON lines with a `v` field (currently `v=2`, DESIGN.md §53). Each unit has one **current** file plus rotated generations.

File name: reversible percent-encoding of the lower-case unit name so Windows-forbidden characters cannot collide (`foo:bar` → `foo%3Abar.log`, `foo*bar` → `foo%2Abar.log`). Reserved device basenames (`CON`/`PRN`/`AUX`/`NUL`/`COM1`–`9`/`LPT1`–`9`) and trailing dots or spaces are percent-encoded the same way. Map keys and files use the normalized name (DESIGN.md §36). `logs` / `Read` also filter by the record `unit` field so a mixed or colliding file cannot bleed lines across units.

Fields stored in `v=2`:

```text
timestamp
unit
pid
stream
message
invocation ID
severity
session
user SID
```

`v=1` lines (timestamp, unit, pid, stream, message, invocation ID) remain readable. Missing `severity` / `session` / `user SID` decode as empty. Writers emit `v=2` and do not rewrite old files.

`severity` is mapped from `stream` (`stdout` → `info`, `stderr` → `err`). There is no unit-file severity directive. `session` and `user SID` come from the user-manager / unit runtime when known; system-scope lines leave them empty.

The writer **keeps the current file open**. Lines are buffered and flushed on a short timer (and whenever logs are read). `Sync` (fsync) runs on unit capture end, daemon shutdown, and rotate — **not** per line.

`logs` / `Query` flushes the current file under the per-unit write lock, then scans and decodes **without** holding that lock, so a follower cannot stall `capture` beyond a flush. The current file is append-only; reading it unlocked is safe. A follow poll skips `entryID` for lines whose timestamp is strictly before the cursor, and skips rotated archives whose last entry precedes the cursor.

A per-unit “new data” signal from the writer (so Follow blocks instead of polling) and a byte offset in the cursor (so a follow poll reads only the tail) are later.

Per-unit rotation: the current file is capped at **10 MiB**. On rotate it becomes `.log.1`; older generations shift up to `.log.3` (**keep 3** rotated files). `.log.3` is dropped on the next rotate. `logs` reads oldest-to-newest across those files.

### CLI

```powershell
winctl logs foo
winctl logs foo --follow
winctl logs foo --since "1 hour ago"
```

`--since` is honored on the daemon (`LogsParams.Since`). Accepted values: RFC3339 (nano or second), a `YYYY-MM-DD` date (UTC midnight), a unit-file duration subtracted from now (`1h`, `1d`, `30min`, `1h 30min`), and the same duration with a trailing `ago` (`1 hour ago`, `1 day 2 hours ago`). Invalid values are `invalid-params`, not a silent ignore.

`--follow` is client polling with an opaque cursor (`LogsParams.Cursor` / `LogsResult.Cursor`). Each poll may wait briefly for new lines. The wait deadline uses the manager clock (the same `now` as `--since` relative times), so a fake clock can expire it. There is no streaming RPC.

`--boot` is not implemented.

Avoid using the name `journalctl` unless intentional compatibility is desired.

---

## 23. Windows Event Log Integration

Important events should also be emitted into Windows Event Log:

- unit failed
- repeated restart limit reached
- watchdog triggered
- configuration parse failure
- user manager launch failure
- privilege failure

Do not mirror every stdout line into Event Log.

That creates excessive noise and poor performance.

---

## 24. Invocation IDs

Every unit start should receive a unique invocation ID.

Example:

```text
94d93c37-6f04-4e3f-8d5d-a1c01dfe67cf
```

This allows logs to distinguish:

```text
foo.service run #1
foo.service run #2
foo.service run #3
```

Status can expose:

```text
InvocationID=
```

---

## 25. Environment Handling

Support:

```ini
Environment=FOO=bar
EnvironmentFile=C:\ProgramData\app\app.env
```

Potential Windows-specific directives:

```ini
LoadUserEnvironment=yes
ExpandEnvironment=yes
```

Need explicit rules around:

```text
%VARIABLE%
$env:VARIABLE
```

Recommendation:

Unit files should not depend on PowerShell syntax.

Use one unambiguous expansion syntax, for example:

```text
${VARIABLE}
```

---

## 26. Command-Line Parsing

This is a major Windows hazard.

`ExecStart=` parsing must not simply pass strings through whichever shell happens to be installed.

Prefer:

```ini
ExecStart=C:\Program Files\Foo\foo.exe
ExecStartArg=--listen
ExecStartArg=127.0.0.1:8080
```

Or define strict quoting rules.

Potential richer syntax:

```ini
ExecStart=["C:\\Program Files\\Foo\\foo.exe", "--listen", "127.0.0.1:8080"]
```

JSON-array syntax avoids a large class of Windows quoting bugs.

Compatibility syntax could still accept:

```ini
ExecStart=C:\Tools\foo.exe --bar
```

but internally normalize it.

---

## 27. PowerShell Units

Do not implicitly invoke PowerShell.

Explicit:

```ini
ExecStart=pwsh.exe
ExecStartArg=-NoProfile
ExecStartArg=-File
ExecStartArg=C:\Scripts\worker.ps1
```

Possible convenience directive later:

```ini
ExecPowerShell=C:\Scripts\worker.ps1
```

but explicit executable invocation is cleaner.

---

## 28. Security Model

### System manager

Runs as:

```text
LocalSystem
```

or a dedicated highly privileged virtual service account.

### System units

Support:

```ini
User=DOMAIN\svc-app
Group=
```

Prefer virtual service accounts/gMSA where available.

### Privilege reduction

Potential directives:

```ini
IntegrityLevel=low
IntegrityLevel=medium
IntegrityLevel=high

RestrictedToken=yes
AppContainer=yes
```

Longer-term, `winunitd` could become a very useful process sandboxing layer.

---

## 29. Credential Handling

Do not store plaintext passwords in unit files.

Possible credential providers:

- Windows Credential Manager
- LSA secrets
- DPAPI-protected secret store
- gMSA
- virtual service accounts

Possible syntax:

```ini
Credential=sql-password:credman://winunitd/sql-prod
```

Keep secrets separate from declarative unit configuration.

---

## 30. Control Plane

Local control uses a protected Named Pipe.

Example:

```text
\\.\pipe\winunitd\control
```

Per-user managers:

```text
\\.\pipe\winunitd\user\<SID>\control
```

The DACL is defense-in-depth:

- system pipe: LocalSystem and Administrators
- user pipe: that user, LocalSystem, and Administrators

It is not the auth model. `Peer` is derived once at accept from the named-pipe client token. Clients (`winctl` and any in-tree pipe dialer) open the pipe at identification impersonation level (`SECURITY_IDENTIFICATION` / `PipeImpLevelIdentification`), not `SECURITY_ANONYMOUS`, so `ImpersonateNamedPipeClient` can see the connecting caller.

Production path:

1. `ImpersonateNamedPipeClient` on the accepted connection
2. `OpenThreadToken` (`TOKEN_QUERY|TOKEN_DUPLICATE`)
3. Token user SID → `Peer.SID`
4. `CheckTokenMembership` on `BUILTIN\Administrators` → `Peer.Administrator` (an `ImpersonateNamedPipeClient` thread token is already an impersonation token; identification level is enough. A process primary token from the PID fallback is `DuplicateTokenEx`'d to an impersonation token first; `CheckTokenMembership` does not accept a primary token)
5. Token user SID `S-1-5-18` → `Peer.LocalSystem`
6. On a user pipe, token SID matching the pipe owner SID → `Peer.Owner`
7. `RevertToSelf` before any RPC (`LockOSThread` so revert applies to the same OS thread)

Peer is a snapshot at accept: there is no impersonation across RPC, and the token is closed before dispatch.

If impersonation fails (old client that dialed `SECURITY_ANONYMOUS`), the server logs the failure and falls back to `GetNamedPipeClientProcessId` → open the client process (`PROCESS_QUERY_LIMITED_INFORMATION`) → open the process token (`TOKEN_QUERY|TOKEN_DUPLICATE`) → the same SID / Administrators / LocalSystem / Owner derivation. The PID path is not the primary A1 path: handle inheritance and PID reuse would authorize as the opener, not the connecting caller.

A failed impersonation with no usable fallback Peer fails closed (deny / close). `ServeConn` logs the Authorizer error; it does not drop it or proceed as AllowAdmin.

`DefaultAuthorizer` must not stamp `Administrator` on every peer. A user-pipe client is the connecting user (`Owner`); linger and other admin-only methods require `Peer.CanLinger()` (Administrators or LocalSystem). Test authorizers (`AllowAdmin`, `AllowOwner`) are for unit tests only.

Pipe create (system control, user control, and any other winunitd named pipe created the same way, including notify):

- `PIPE_REJECT_REMOTE_CLIENTS` — remote SMB clients cannot connect. This is local IPC.
- `FILE_FLAG_FIRST_PIPE_INSTANCE` (NT `FILE_CREATE` for the first instance) — if the name is already taken, listen fails closed. Do not attach to a squat.

Client squat defense, after connect, before any RPC:

1. `GetNamedPipeServerProcessId`
2. Open the server process (`PROCESS_QUERY_LIMITED_INFORMATION`) and its token (`TOKEN_QUERY`)
3. Token owner SID (and token user SID / `CheckTokenMembership` as below)

System pipe (`\\.\pipe\winunitd\control`): the documented daemon identity is LocalSystem (SCM `Account: LocalSystem`). Accept if token owner is LocalSystem (`S-1-5-18`) or Administrators (`S-1-5-32-544`), or the token user is LocalSystem, or `CheckTokenMembership` on Administrators (elevated console `winunitd`). Mismatch → close; do not send RPCs.

User pipe (`\\.\pipe\winunitd\user\<SID>\control`): the documented user-manager identity is that SID. The user-manager process runs as that user (`winunitd --user-manager <SID>` checks the process token user SID matches). Accept if the server token user SID is that pipe’s SID. Mismatch → close; do not send RPCs.

A failed open of the server process or token fails closed (treat as squat). This does not replace or weaken Peer/Authorizer on the server.

Malformed JSON is answered with `invalid-request`, then the connection closes.

`logs` honors `Follow` and `Since` (see §22). They are not reserved or silently ignored.

Protocol: versioned compact JSON RPC. Do not make CLI output parsing the API.

---

## 31. Remote Control

Not part of MVP.

Later:

```powershell
winctl --host server01 status
```

Could use:

- WinRM
- mutual-TLS RPC
- SSH transport
- Tailscale-addressed HTTPS endpoint

The local daemon protocol should be designed so a remote transport can be added without rewriting the service model.

---

## 32. Runtime State

Separate:

```text
configuration
```

from:

```text
runtime state
```

Runtime state could live under:

```text
C:\ProgramData\winunitd\runtime\
```

User runtime state:

```text
%LOCALAPPDATA%\winunitd\runtime\
```

Persistent metadata:

```text
last run
restart counters
timer state
enabled state
linger state
```

---

## 33. Daemon Reload

Changes to unit files should not silently alter running processes.

Workflow:

```powershell
winctl daemon-reload
winctl restart foo
```

Daemon reload should:

- reparse unit files
- rebuild dependency graph
- validate references
- report cycles
- preserve current process instances unless explicitly restarted

---

## 34. Configuration Validation

Useful command:

```powershell
winctl verify foo.service
```

Or:

```powershell
winctl verify C:\path\foo.service
```

Validate:

- unknown directives
- invalid paths
- dependency cycles
- bad timer syntax
- invalid accounts
- inaccessible working directories
- missing executables

---

## 35. Dependency Cycle Diagnostics

Do not merely report:

```text
cycle detected
```

Report:

```text
foo.service
  After=bar.service

bar.service
  After=database.service

database.service
  After=foo.service
```

and explain the edge causing the cycle.

Operational diagnostics should be treated as a first-class feature.

---

## 36. Unit Aliases

Support:

```text
foo.service
foo
```

where unqualified names default to `.service`.

Unit names are **case-insensitive** and are normalized to **lower-case** at load and at every entry point: CLI arguments, `Requires=` / `Wants=` / `After=` / `Before=` / `WantedBy=`, companion basename activation (`.timer` / `.registry` / `.eventlog` / `.path` → `.service`), and enable keys. Map keys and journal files use the normalized name. Display may keep the on-disk path. Two unit files that differ only in case are a load error.

Possible aliases:

```ini
Alias=myapp.service
```

---

## 37. Templates

Useful later:

```text
worker@.service
```

Instantiation:

```powershell
winctl start worker@1
winctl start worker@2
```

Variable:

```text
%i
```

Example:

```ini
ExecStart=C:\worker.exe
ExecStartArg=--instance
ExecStartArg=%i
```

This is extremely useful for homogeneous workers.

---

## 38. Conditions

Useful conditions:

```ini
ConditionPathExists=
ConditionPathIsDirectory=
ConditionEnvironment=
ConditionArchitecture=
ConditionUser=
ConditionNetworkAvailable=
```

Windows-specific:

```ini
ConditionDomainJoined=yes
ConditionInteractiveSession=yes
ConditionRdpSession=yes
```

Conditions should skip a unit cleanly rather than mark it failed.

---

## 39. Triggers

Potential future trigger types:

### File

File watches stay on `.path` (see §6.4). Do not overload `.path` for registry. `PathChanged=` (OR) and `PathExists=` (AND) are implemented. `PathExistsIsDirectory=` is later.

```ini
[Path]
PathChanged=C:\Data\incoming
PathExists=C:\Data\incoming\ready.flag
```

`PathChanged=` is an absolute Windows path. Repeatable values are OR. `PathExists=` is an absolute Windows path. Repeatable values are AND (every listed path must exist; this is a lock versus systemd OR). Mixing both on one unit: either may start the counterpart. `ReadDirectoryChangesW` is non-recursive. A file path watches the parent directory and filters by name.

### Registry ★

Windows-native companion (see §6.6). Implemented as `.registry`, not as a `.path` variant.

```ini
[Registry]
RegistryChanged=HKLM\Software\Example
```

### Event Log ★

Windows-native companion (see §6.7). Implemented as `.eventlog`.

```ini
[EventLog]
EventLogTrigger=System:EventID=1234
```

### Device

```ini
DeviceArrival=USB\VID_....
```

These would be useful Windows-native extensions that systemd itself does not map directly.

---

## 40. Network Targets

Windows startup has a recurring ambiguity:

```text
network stack exists
```

versus:

```text
useful network connectivity exists
```

Provide separate semantics:

```text
network.target
network-online.target
```

Potential implementation of `network-online.target`:

- Network List Manager
- IP Helper API
- configurable probe
- DNS availability
- route availability

Avoid hard-coding "internet access" as the definition of network readiness.

---

## 41. Sleep / Resume

Units should be able to react to power transitions.

Potential targets:

```text
sleep.target
suspend.target
resume.target
```

Potential unit behavior:

```ini
After=resume.target
```

Could be implemented using Windows power broadcast notifications.

Timers must also correctly account for sleep intervals.

---

## 42. Shutdown

`winunitd` should receive SCM preshutdown notification and perform dependency-aware shutdown.

There are two stop plans:

1. **Shutdown** (root = `shutdown.target`): stop every active unit, ordered by reverse `After=` / `Before=`. This is the manager-stop transaction on `sc stop`, preshutdown, or console SIGINT.
2. **Single-unit stop** (`winctl stop foo` / `PlanStop`): stop `foo` plus its reverse requirement closure only — units that `Requires=` / `BindsTo=` / `PartOf=` `foo` (dependents that cannot run without it), dependents first. Do not stop foo's forward `Requires=` / `Wants=`. Do not stop pure `After=` / `Before=` neighbors.

Expected shutdown order:

```text
reverse After=/Before= order
```

Example:

```text
web.service
    |
database.service
```

Startup:

```text
database -> web
```

Shutdown:

```text
web -> database
```

Use SCM preshutdown support to obtain more than the minimal normal service shutdown window.

---

## 43. Resource Control

Job Objects make job-wide resource limits realistic. Directives apply
to the unit's existing Job Object (the whole process tree, not the
main PID only). Type=scm and Type=scheduled-task have no job; those
keys are ignored with a verify warning, matching R1.

R1 applies:

```ini
MemoryMax=2G
ProcessLimit=32
PriorityClass=below-normal
```

- `MemoryMax=` is a job-wide commit cap (`JOB_OBJECT_LIMIT_JOB_MEMORY`).
  Suffixes are `K` / `M` / `G` (1024-based), for example `2G`.
- `ProcessLimit=` is the maximum number of active processes in that job.
- `PriorityClass=` is `idle`, `below-normal`, `normal`, `above-normal`,
  or `high`. `realtime` is rejected.
- Omitting all resource keys leaves today's job (no extra Job Object limits).
- Hitting `MemoryMax=` or `ProcessLimit=` fails the unit with reason
  `resource-limit` (`winctl status`, §44). `Restart=` still applies,
  including `on-failure`. Start-limit still applies to those relaunches.

R2 applies:

```ini
CPUWeight=50
CPUQuota=25%
IoPriority=low
```

- `CPUWeight=` is an integer 1–10000. It maps to Job Object weight-based
  CPU rate (Windows weight 1–9):
  `weight = clamp(1, 9, (CPUWeight + 1110) / 1111)` (integer division).
  Examples: `CPUWeight=50` → 1; `CPUWeight=5000` → 5; `CPUWeight=10000` → 9.
  `ControlFlags` are `JOB_OBJECT_CPU_RATE_CONTROL_ENABLE |
  JOB_OBJECT_CPU_RATE_CONTROL_WEIGHT_BASED`.
- `CPUQuota=` is `N%` with N in 1–100 (percentage of total machine CPU).
  The trailing `%` is required. This is Job Object semantics, not systemd
  per-CPU `CPUQuota=` (systemd `200%` means two CPUs; here `100%` is
  already the whole machine). It maps to a hard-cap `CpuRate = N * 100`
  (hundredths of a percent), so `CPUQuota=25%` is `CpuRate=2500` and
  `CPUQuota=100%` is `CpuRate=10000`. Windows `CpuRate` max is 10000.
  Out-of-range N (`0`, `>100`) is a parse/verify error; there is no
  silent clamp at start. `ControlFlags` are
  `JOB_OBJECT_CPU_RATE_CONTROL_ENABLE | JOB_OBJECT_CPU_RATE_CONTROL_HARD_CAP`.
- `CPUWeight=` and `CPUQuota=` cannot both be set (one CPU control mode
  per unit).
- `IoPriority=` is `idle`, `low`, `normal`, or `high`. Job Objects have
  no I/O-priority information class. R2 sets
  `NtSetInformationProcess(ProcessIoPriority)` on the main process after
  it is assigned to the unit job and while it is still `CREATE_SUSPENDED`,
  so the process stays in the job. Mapping: `idle`→`IoPriorityVeryLow`(0),
  `low`→`IoPriorityLow`(1), `normal`→`IoPriorityNormal`(2),
  `high`→`IoPriorityHigh`(3). A failed set fails activation with reason
  `configuration` (no silent no-op). Children inherit I/O priority at
  `CreateProcess`.

These are Windows Job Object / process I/O-priority semantics, not cgroup
`cpu.weight` / `cpu.max` / `io.weight` and not systemd per-CPU `CPUQuota=`.
A Windows weight 1–9 is not a cgroup weight. `CPUQuota=25%` is a Job Object
`CpuRate` of 2500 (25.00% of total machine CPU in hundredths-of-percent),
not cgroup `cpu.max`.

---

## 44. Restart on Resource Failure

Possible failure reasons:

```text
exit-code
signal-equivalent
watchdog
timeout
resource-limit
dependency
start-limit
credential
configuration
```

Expose them consistently through:

```powershell
winctl status
winctl show
```

---

## 45. Status Output

Example:

```text
PS> winctl status hermes

● hermes.service - Hermes Agent
     Loaded: loaded (C:\ProgramData\winunitd\units\hermes.service; enabled)
     Active: active (running) since Mon 2026-08-31 10:24:11 CEST
      Scope: user
      Owner: ALICE
   Main PID: 4216
        Job: active
      Tasks: 4
     Memory: 284 MB
     Health: healthy
   Watchdog: notify (last heartbeat 4s ago)
   Restarts: 1

Aug 31 11:03:18 hermes[4216]: Connected to remote agent host
Aug 31 11:03:22 hermes[4216]: Worker pool ready
```

---

## 46. Machine Status

```powershell
winctl status
```

Could show:

```text
winunitd
  State: running
  Units: 38 loaded
         21 active
          2 failed
  Timers: 7 active
  Users:  3 managers
          1 lingering
  Since:  2026-08-31 07:41:02
```

---

## 47. Failure UX

Useful:

```powershell
winctl --failed
```

Output:

```text
UNIT              STATE    REASON
backup.service    failed   exit-code 3
crawler.service   failed   watchdog
sync.timer        failed   trigger unit failed
```

Then:

```powershell
winctl status backup
winctl logs backup
```

---

## 48. Install / Packaging

Ideal install:

```text
winunitd.msi
```

Installer actions:

1. Install binaries
2. Create `C:\ProgramData\winunitd`
3. Register `winunitd` Windows Service
4. Configure preshutdown support
5. Configure Event Log provider
6. Create control pipe ACLs
7. Start service
8. Add `winctl.exe` to PATH

Portable install could exist later.

---

## 49. Windows Service Registration

Only one permanent SCM entry:

```text
ServiceName: winunitd
DisplayName: WinUnit Manager
Startup: Automatic (Delayed or Triggered depending design)
Account: LocalSystem
```

Potential service dependencies should be minimal.

Avoid dependency on Task Scheduler.

---

## 50. Migration Helpers

A practical adoption feature would be import helpers.

### Existing Windows Service

```powershell
winctl import-service MyLegacyService
```

Generate:

```ini
[Unit]
Description=Imported MyLegacyService

[Service]
Type=external-service
ServiceName=MyLegacyService
```

Alternatively, `winunitd` could manage SCM services as proxy units.

### Scheduled Task

```powershell
winctl import-task "\Backups\Nightly"
```

Generate:

```text
nightly-backup.service
nightly-backup.timer
```

This could be a major usability win.

---

## 51. External SCM Service Units

Useful compatibility unit:

```ini
[Service]
Type=scm
ServiceName=MSSQLSERVER
```

`Type=scm` orchestrates an existing named SCM service. `winctl start` calls StartService, `winctl stop` calls StopService, and status is QueryServiceStatusEx mapped to ActiveState. Already running is a successful start; already stopped is a successful stop. This does not register, change, or delete SCM configuration (`import-service` is a separate helper and is not this unit type).

System manager only: `Type=scm` in a user unit is a load/start error.

`TimeoutStartSec` and `TimeoutStopSec` wait for the corresponding SCM state, then fail.

`Restart=` applies to SCM start failure the same way as a process start failure. After the service reports running, winunitd does not supervise or restart it (SCM recovery stays in charge).

Then dependencies can target legacy services:

```ini
Requires=mssql.service
After=mssql.service
```

This lets `winunitd` orchestrate both native WinUnit workloads and existing SCM services.

---

## 52. Task Scheduler Interop

Task Scheduler is an external integration, not the core scheduler. Native `.timer` units remain the recommended path for new schedules. `Type=scheduled-task` is interop for an already-registered legacy task only.

```ini
[Service]
Type=scheduled-task
TaskName=\Backups\LegacyBackup
```

`Type=scheduled-task` orchestrates an existing registered task. `winctl start` calls ITaskService / IRegisteredTask.Run (run the task now). `winctl stop` ends running instance(s) for that task (IRegisteredTask.Stop). Status is IRegisteredTask.State plus running instances, mapped to ActiveState. Already running is a successful start; already stopped is a successful stop. This does not create, edit, delete, enable, or disable the task definition (`import-task` is a separate helper and is not this unit type). There is no `ExecStart=` / `ExecStartArg=` (present with this Type is a verify error).

System manager only: `Type=scheduled-task` in a user unit is a load/start error.

`TimeoutStartSec` and `TimeoutStopSec` wait for the corresponding Task Scheduler state, then fail. Start waits until the task is running (TASK_STATE_RUNNING or a running instance exists). Stop waits until there are no running instances (READY or DISABLED).

`Restart=` applies to start failure (failed Run) the same way as a process start failure. After the task reports running, winunitd does not supervise or restart it (Task Scheduler owns the instance).

A missing registered task is a start failure (and `winctl status` overlays `failed`). Parse/verify only requires `TaskName=`; it does not check that the task exists.

ActiveState mapping:

| Task Scheduler | ActiveState |
| --- | --- |
| TASK_STATE_RUNNING, or GetInstances count > 0 | active |
| TASK_STATE_QUEUED | activating |
| TASK_STATE_READY | inactive |
| TASK_STATE_DISABLED | inactive |
| TASK_STATE_UNKNOWN / other, or Query/open failure | failed |

Then dependencies can target the proxy:

```ini
Requires=legacy-backup.service
After=legacy-backup.service
```

This lets `winunitd` orchestrate both native WinUnit workloads and existing Task Scheduler tasks without making Task Scheduler the core timer engine.

---

## 53. API Stability

Define clear compatibility levels:

```text
unit file format version
control protocol version
journal format version
```

Example:

```ini
[Unit]
FormatVersion=1
```

Unknown directives should normally be warnings, not silent ignores.

Strict mode:

```powershell
winctl verify --strict
```

---

## 54. Unit File Syntax Strategy

Two reasonable options exist.

### Option A — systemd-like INI

Advantages:

- familiar
- readable
- terse
- ecosystem knowledge already exists

Disadvantages:

- legacy quoting and escaping can be awkward on Windows

### Option B — YAML/TOML/JSON

Advantages:

- richer typing
- arrays simplify Windows argument handling

Disadvantages:

- less familiar operational model
- YAML brings its own parsing problems

Recommendation:

Use systemd-like INI for human configuration, but allow directives that avoid ambiguous command-line parsing.

Example:

```ini
ExecStart=C:\Program Files\App\app.exe
ExecStartArg=--config
ExecStartArg=C:\ProgramData\App\config.json
```

---

## 55. Windows-Specific Extension Namespace

Avoid pretending every concept is portable.

Potential namespace:

```ini
WinSessionMode=
WinIntegrityLevel=
WinAppContainer=
WinDesktop=
WinPriorityClass=
```

Or simply use clear names without `Win` prefixes where ambiguity is low.

The unit model should be inspired by systemd, not trapped by it.

---

## 56. Executable Discovery

Do not silently search arbitrary directories.

Recommended:

- absolute path preferred
- `PATH` lookup allowed only when explicitly enabled or clearly specified
- status output should show resolved executable path

Possible:

```ini
SearchPath=yes
```

Default:

```ini
SearchPath=no
```

This improves reproducibility and security.

---

## 57. Working Directory

Default behavior should be explicit.

Possible default:

```text
unit file directory
```

but safer:

```text
C:\Windows\System32
```

is technically traditional but operationally terrible.

Recommendation:

Require or strongly encourage:

```ini
WorkingDirectory=
```

Status should warn if a writable unexpected directory is used.

---

## 58. Startup Environment

A lingering user's environment cannot be assumed to match an Explorer-launched process.

Therefore user services should receive a deterministic environment.

Potential variables:

```text
USERPROFILE
LOCALAPPDATA
APPDATA
TEMP
TMP
USERNAME
USERDOMAIN
```

Optional:

```ini
ImportInteractiveEnvironment=yes
```

This could merge selected variables from the latest interactive session.

Default should be deterministic and independent of login.

---

## 59. GUI Applications

GUI apps should not run from the machine manager.

For GUI user units:

```ini
RequiresInteractiveSession=yes
```

The user manager should attach the process to an appropriate interactive session.

Possible target:

```text
graphical-session.target
```

GUI applications should stop or detach according to declared policy when the session ends.

---

## 60. Multiple User Sessions

Need defined semantics for:

```text
same user logged into console + RDP
```

Possible policies:

```ini
InstancePerSession=no
InstancePerSession=yes
```

Default user services:

```text
one instance per user
```

GUI/session services:

```text
one instance per session
```

Template instance name might include session ID.

---

## 61. Notifications

Optional future support:

```ini
FailureAction=notify
```

Could integrate with:

- Windows toast notifications
- email
- webhook
- Teams/Slack
- generic command hooks

Keep external notification plugins outside core daemon where possible.

---

## 62. Hooks

Support:

```ini
ExecStartPre=
ExecStartPost=
ExecStop=
ExecStopPost=
ExecReload=
```

Each should have well-defined failure semantics.

Example:

```ini
ExecStartPre=C:\Tools\check-config.exe
```

If it fails, main process does not launch.

---

## 63. Failure Hooks

Useful:

```ini
OnFailure=alert-admin.service
```

This can remain unit-driven rather than hard-coded notification logic.

Example:

```ini
[Unit]
OnFailure=unit-failure-webhook@%n.service
```

---

## 64. Secrets Passed to Processes

Avoid environment variables for highly sensitive secrets where possible.

Potential feature:

```ini
LoadCredential=db-password:credman://prod/db
```

Runtime could materialize credential into:

```text
named pipe
temporary restricted file
inherited handle
```

This is a later-stage feature but architecturally worth reserving.

---

## 65. Testing Strategy

The project needs aggressive lifecycle testing.

Test matrix:

- clean boot
- service crash
- repeated crash
- child process spawning
- escaped child attempt
- machine sleep
- hibernation
- resume
- user login
- user logout
- multiple sessions
- RDP disconnect
- RDP reconnect
- network loss
- clock jump
- DST transition
- shutdown
- forced reboot
- watchdog failure
- daemon crash/restart

The hardest bugs will be state transition bugs rather than parser bugs.

---

## 66. Persistence and Crash Recovery

If `winunitd` itself crashes:

- SCM should restart it
- daemon reconstructs runtime state
- surviving child processes need defined behavior

Two possible models:

### Strict ownership

Children die with daemon Job Objects.

Pros:
- simple
- deterministic

Cons:
- daemon crash restarts all workloads

### Persistent ownership

More complex detached supervisor structure.

Recommendation for initial implementation:

Use strict ownership.

A daemon crash should be rare and should produce deterministic restart behavior.

---

## 67. Self-Watchdog

SCM recovery configuration:

```text
restart winunitd after failure
```

Potential Windows Service recovery:

```text
1st failure: restart
2nd failure: restart
subsequent: restart
```

Because `winunitd` is infrastructure, fast recovery is appropriate.

---

## 68. State Machine

Each unit should have an explicit state machine.

Example:

```text
inactive
   |
activating
   |
active
   |
deactivating
   |
inactive
```

Failure transitions:

```text
activating -> failed
active -> failed
deactivating -> failed
```

Substates:

```text
start-pre
start
start-post
running
stop
stop-post
auto-restart
watchdog
```

Do not model lifecycle as a collection of booleans.

---

## 69. Concurrency

Dependency engine should support parallel startup when ordering permits.

Example:

```text
database.service     redis.service
       \                /
        \              /
          api.service
```

`database` and `redis` may start concurrently.

Do not serialize the entire boot graph.

---

## 70. Transaction Model

Starting a unit should construct a transaction:

```text
requested unit
dependencies
conflicts
ordering
jobs
```

Then validate before executing.

This prevents partially-applied dependency plans.

Command:

```powershell
winctl start foo --dry-run
```

Could show:

```text
START database.service
START redis.service
START foo.service
```

---

## 71. Why Not Just Wrap SCM?

SCM is useful but insufficient as the actual orchestration engine.

Limitations include:

- limited dependency semantics
- no rich user runtime
- no lingering model
- no integrated timers
- no generic readiness/watchdog protocol
- weak structured process-tree ownership
- fragmented diagnostics
- service registration required per workload

Therefore:

```text
SCM should supervise winunitd
winunitd should supervise workloads
```

---

## 72. Why Not Task Scheduler?

Task Scheduler is suitable for many standalone scheduled jobs but awkward as a service/runtime framework.

Problems:

- separate lifecycle model
- task-specific security semantics
- awkward persistent user-runtime behavior
- weak service-style dependency graph
- poor unified status model
- no integrated watchdog supervision
- no single configuration/runtime abstraction

It should remain an interop target, not the core.

---

## 73. Why Not NSSM / WinSW / Servy?

These solve the "run this executable as a service" problem.

`winunitd` solves:

```text
manage a graph of long-lived and scheduled workloads
```

The distinction is architectural.

A useful compatibility story would allow imported/wrapped services, but `winunitd` should not simply become another service wrapper.

---

## 74. MVP

The first actually useful version should support only:

### Daemon

- Windows Service host
- Named pipe control API
- Unit loader
- Dependency graph
- Job Object process supervision
- restart policies
- system `.service`
- system `.target`
- system `.timer`
- logging
- status

### CLI

```text
start
stop
restart
status
enable
disable
list-units
list-timers
logs
daemon-reload
verify
```

### Service directives

```text
Description=
Requires=
Wants=
After=
Before=
ExecStart=
WorkingDirectory=
Environment=
Restart=
RestartSec=
StartLimitIntervalSec=
StartLimitBurst=
TimeoutStartSec=
TimeoutStopSec=
WantedBy=
```

### Timer directives

```text
OnBootSec=
OnStartupSec=
OnUnitActiveSec=
OnCalendar=
Persistent=
```

This would already be materially better than the Windows status quo.

---

## 75. Phase 2

Add:

- user managers
- lingering
- user units
- session tracking
- native readiness notify
- watchdog heartbeats
- TCP/HTTP health checks
- invocation IDs
- richer logs
- SCM proxy units

At this point the project becomes genuinely distinctive.

---

## 76. Phase 3

Add:

- path units — `.path` `PathChanged=` (OR) and `PathExists=` (AND) (★; `PathExistsIsDirectory=` later)
- registry/event triggers — `.registry` and `.eventlog` are the Windows-native companions (★)
- templates
- remaining resource limits — R2: CPUWeight=, CPUQuota=, IoPriority= (implemented; Windows Job Object / ProcessIoPriority semantics, not cgroups)
- credentials
- session-scoped GUI units
- remote control
- migration tools
- Task Scheduler proxy/import
- named-pipe/socket activation

---

## 77. Suggested Implementation Language

Good candidates:

### Rust

Advantages:

- strong Windows API bindings
- robust concurrency
- memory safety
- excellent for a long-running privileged daemon
- straightforward static deployment

Likely best fit.

### Go

Advantages:

- simpler implementation
- good concurrency model
- easy static distribution

Disadvantages:

- some lower-level Windows behavior may require more custom syscall work
- Windows token/session APIs can become awkward

Still very viable.

### C#

Advantages:

- excellent Windows integration
- easy service development
- strong Event Log / management APIs

Disadvantages:

- Job Objects and advanced token work still require P/Invoke
- runtime/deployment model somewhat heavier

For a project intended as Windows infrastructure, **Rust is probably the strongest technical fit**, with Go as the fastest implementation path.

---

## 78. Internal Components

Possible crate/package layout:

```text
winunitd-core
    unit parser
    dependency graph
    state machine

winunitd-runtime
    process execution
    jobs
    tokens
    sessions

winunitd-timers
    calendar parser
    monotonic scheduling

winunitd-journal
    structured logs

winunitd-protocol
    control API

winunitd-service
    SCM host

winctl
    CLI
```

Keep parser/state logic independently testable from Windows APIs.

---

## 79. Suggested Executables

```text
winunitd.exe
winctl.exe
winunit-notify.exe
```

Optional:

```text
winunit-notify.exe
```

lets arbitrary scripts/apps send:

```powershell
winunit-notify --ready
winunit-notify --watchdog
winunit-notify --status "Waiting for work"
```

This makes readiness/watchdog integration trivial for scripts.

---

## 80. Example Notify Flow

Unit:

```ini
[Service]
Type=notify
ExecStart=C:\App\worker.exe
WatchdogSec=30s
```

Environment injected:

```text
WINUNIT_NOTIFY_PIPE=\\.\pipe\winunitd\notify\94d93...
WINUNIT_WATCHDOG_USEC=30000000
```

App sends:

```text
READY=1
STATUS=Connected
```

then periodically:

```text
WATCHDOG=1
```

Very close to `sd_notify`, but Windows-native.

---

## 81. Naming

`winunitd` is a good daemon name because it describes the abstraction rather than copying `systemd`.

`winctl` is concise and obvious.

Potential terminology:

```text
WinUnit
Unit Manager
WinUnit daemon
```

Avoid calling it "Windows systemd."

It should be described as:

> A declarative Windows unit manager and process supervisor.

---

## 82. Example End-to-End Workflow

Install application:

```text
C:\Apps\Hermes\
```

Create:

```text
%LOCALAPPDATA%\winunitd\units\hermes.service
```

Then:

```powershell
winctl --user daemon-reload
winctl --user enable hermes
winctl --user start hermes
```

Inspect:

```powershell
winctl --user status hermes
winctl --user logs hermes --follow
```

Enable persistence:

```powershell
winctl enable-linger Alice
```

Now Hermes:

- starts without interactive login
- restarts on failure
- belongs to one Job Object
- has centralized logs
- can expose readiness
- can be watchdogged
- is managed through one declarative unit

That is exactly the operational gap the project is intended to fill.

---

## 83. The Main Architectural Principle

The project should not attempt to make Windows Services nicer.

It should build a **real workload manager above Windows primitives**.

Windows already provides most of the necessary mechanisms:

```text
SCM                -> bootstrap winunitd
Job Objects        -> process ownership
Access Tokens      -> identity
Session APIs       -> user/session awareness
Named Pipes        -> local control and notify
Waitable Timers    -> scheduler
ETW/Event Log      -> diagnostics
ReadDirectoryChangesW -> path triggers
Credential Manager / DPAPI -> secrets
```

The missing component is the coherent orchestration layer.

That layer is `winunitd`.

---

## 84. Practical Definition of Success

A successful first mature release should make this normal on Windows:

```powershell
winctl --user enable-linger
winctl --user enable hermes.service
winctl --user enable backup.timer
winctl --user start default.target
```

and make this unnecessary:

- Startup folder entries
- hand-created Scheduled Tasks
- NSSM wrappers
- WinSW XML per application
- custom "install service" scripts
- bespoke watchdog scripts
- tray apps whose only purpose is keeping a process alive
- one-off PowerShell startup orchestration

The desired result is not perfect systemd compatibility.

The desired result is **one coherent, declarative runtime model for Windows background workloads**.
