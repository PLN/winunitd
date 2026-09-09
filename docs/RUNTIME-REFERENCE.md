# Runtime and advanced features

This reference includes experimental features. Start with the [README](../README.md)
for the supported beta scope and installation. See the [unit reference](UNIT-REFERENCE.md)
for syntax and [qualification](BETA-QUALIFICATION.md) for tested behavior.

Manual service registration below is for development installations. Do not use
those commands against an MSI installation; follow the [MSI procedure](../packaging/beta/INSTALL.md).

## Windows Service

```text
winunitd install [--base-dir DIR]
winunitd uninstall
```

Install creates `C:\ProgramData\winunitd` (`units\`, `enabled\`, `journal\`, `runtime\`, `linger\`) if missing, sets SCM recovery to restart on failure, and accepts preshutdown for ordered stop. It does not add PATH or register an Event Log provider.

On start (SCM or console) the daemon starts `builtin` `default.target`, which Wants=`timers.target`. Built-in targets: `default.target`, `timers.target`, `shutdown.target` (`network-online.target` is not shipped). A user manager also loads `graphical-session.target`.

The daemon creates a Job Object with `KILL_ON_JOB_CLOSE`: if `winunitd.exe` is killed, assigned children die with it. Each started unit gets its own nested Job Object (no breakaway). On `sc stop`, preshutdown, or console SIGINT, units stop in reverse After=/Before= order (`shutdown.target` as the stop root), then the daemon Job Object closes.

`winctl daemon-reload` reparses units and rebuilds the graph without dropping live jobs.
Invalid files or an ordering cycle reject the entire candidate; the previous
accepted definitions and graph remain usable. A rejected cold load starts no
candidate units. A valid removal retains a live unit as `LoadState=unavailable`
for status/log/stop access and suppresses future activation until its definition
returns. Reload errors and cycle diagnostics are returned by `daemon-reload`.
Machine and unit status report `ConfigRevision`; service status also reports
`InvocationConfigRevision` for its last captured service invocation. Reload and
enablement graph acceptance produce a new opaque ID, including unchanged-content
reloads. Running services and automatic recovery keep their captured ID; a new
explicit start adopts the accepted one. A removed unit has no loaded revision,
but its last invocation revision remains available. IDs are scoped across daemon
instances and do not expose configuration content.
Timer and native-watch status report `ArmedConfigRevision` until disarm/cleanup.
Reload keeps that captured ID; a fresh arm adopts the accepted revision.
`list-timers` reports the armed companion when a timer remains armed across reload.

## Control API

Versioned JSON-RPC on `\\.\pipe\winunitd\control` (LocalSystem and Administrators). Production `Peer` comes from the named-pipe client token. Pipes use `PIPE_REJECT_REMOTE_CLIENTS` and first-instance-only; `winctl` verifies the server process owner after connect and refuses a squat.

`winctl --user …` and `winctl <command> --user` both use `\\.\pipe\winunitd\user\<SID>\control`. Bare `winctl` stays on the system pipe. Linger admin verbs do not use `--user`.

### `winctl` commands

`start`, `stop`, `restart`, `status`, `operation`, `cancel`, `enable`, `disable`, `list-units`, `list-timers`, `logs`, `daemon-reload`, `enable-linger`, `disable-linger`, `verify`.

Unit status and list-units responses add optional `subState` and
`terminationUncertain` fields. `winctl status` shows the manager phase and
`Termination: unconfirmed; retry stop` while owned cleanup is pending or failed,
even when there is no error string yet. A successful stop clears uncertainty.
The phase belongs to manager lifecycle; a native proxy's ActiveState is a separate
external observation. Missing fields from an older daemon remain supported.
These fields do not claim immutable aggregate snapshots or new health semantics.

`winctl status UNIT` exit codes are systemctl-shaped: **0** active, **3** loaded but inactive/failed, **4** not loaded. Transport/protocol errors keep their existing non-zero exit. Machine status (no unit) exits 0 on success.

Start/stop/restart replies and admitted failures include `OperationID`; unit
status exposes `LastOperationID`. Use `winctl operation ID` to query that
transaction's outcome independently of current unit state. The manager retains
pending operations and its latest 256 completed records; history resets when
the manager exits. See [operation history](OPERATIONS.md) for admission
budgets, query exit codes, compatibility, and current limits.

### Enable links

`winctl enable` writes files under `<base-dir>\enabled\<target>\<unit>` (not NTFS symlinks). A fresh daemon start pulls in enabled Wants=/Requires= only.

## Unit files

### Verify

`winctl verify` parses a systemd-like INI unit and checks MVP rules. It does not need a running daemon when given a file path:

```text
winctl verify C:\path\foo.service
winctl verify C:\path\foo.timer
winctl verify C:\path\foo.registry
winctl verify C:\path\foo.eventlog
winctl verify C:\path\foo.path
```

Unknown directives are errors. `ExecStart=` must be an absolute Windows path (`SearchPath=no`). An omitted `WorkingDirectory=` is a warning (System32 is not the default). `Environment=` values are literals (`${}` is not expanded).

Path vs unit name: a name with no `/`, `\`, or drive prefix is always a unit name, even if a same-named file exists in the cwd. Use `./foo.service`, `.\foo.service`, `C:\path\foo.service`, or `winctl verify --file foo.service` (`-f`). Mixing unit names and paths in one invocation is a usage error. `winctl verify foo.service` (unit name) talks to the daemon.

### Restart and start limits

`Restart=` supports `no` / `always` / `on-failure` / `on-watchdog`.

`[Unit]` `StartLimitIntervalSec=` / `StartLimitBurst=` default to 10s / 5. `StartLimitBurst=0` is unlimited. Burst starts inside the interval fail with reason `start-limit`. Timer/watch activations and automatic recovery share this budget; a redundant activation of an already-running process does not consume another start. Explicit `winctl start` resets the limit.

`RestartMaxDelaySec=` and `RestartBackoff=` are not parsed.

The daemon admits up to 32 simultaneous start transactions, with up to 16
concurrent adapter calls per transaction. Excess operator starts report capacity
exhaustion and may be retried. Explicit stops use a separate 32-transaction
budget; shutdown bypasses both budgets. Native watch activations
wait for capacity; timer activations retry without consuming a second deadline.
These pending retries are not yet durable across a daemon crash.

### Invocation IDs

Each unit start (including `Restart=` relaunch) gets a new UUID. `winctl status` prints `InvocationID=`. Journal lines for that run carry the same ID. The process env includes `WINUNIT_INVOCATION_ID`.

### Notify and watchdog

`Type=notify` stays activating until the unit writes `READY=1` on `WINUNIT_NOTIFY_PIPE` (`\\.\pipe\winunitd\notify\<unit>`), or `TimeoutStartSec` fires. `NotifyAccess=main` only. Scripts send `READY` / `WATCHDOG` / `STATUS` with `winunit-notify.exe`.

The current notify transport sends a `WINUNITD-NOTIFY/1` line from the server before the client writes its payload. Clients must read this acceptance banner before sending and closing, preventing a short-lived Windows pipe connection from being discarded before acceptance. The banner is not a readiness acknowledgement. Upgrade `winunit-notify.exe` and the daemon together: older daemons do not send it, so the current helper will time out against them. Custom clients should adopt the handshake; legacy write-only clients retain the early-disconnect risk.

| Mode | Behavior |
| --- | --- |
| `WatchdogMode=notify` (default with `WatchdogSec=`) | Injects `WINUNIT_WATCHDOG_USEC`; missed heartbeat fails the unit |
| `WatchdogMode=tcp` | Connect-only on `WatchdogEndpoint=` (`127.0.0.1:port` or `[::1]:port`) |
| `WatchdogMode=http` | GET `WatchdogEndpoint=` (`http://127.0.0.1:...`); expects `WatchdogExpectedStatus=` (default 200) |

TCP/HTTP endpoints must be loopback; non-loopback is a parse error and is never dialed. A missed or failed probe fails the unit (`failed` / `watchdog`), then `Restart=` (including `on-watchdog`).

### Resource limits (Job Object)

Limits apply on the existing per-unit job. Hitting `MemoryMax=` or `ProcessLimit=` fails the unit with reason `resource-limit` on `winctl status`; `Restart=` still applies.

| Directive | Semantics |
| --- | --- |
| `MemoryMax=` | Job-wide commit cap (`K` / `M` / `G`) |
| `ProcessLimit=` | Active processes |
| `PriorityClass=` | `idle` / `below-normal` / `normal` / `above-normal` / `high` (not `realtime`) |
| `CPUWeight=` | 1–10000 → Windows job weight 1–9 as `clamp(1, 9, (CPUWeight + 1110) / 1111)` |
| `CPUQuota=` | `N%` with N in **1–100** = percentage of **total machine CPU** → `CpuRate = N * 100` (Windows max 10000; not systemd per-CPU) |
| `IoPriority=` | `idle` / `low` / `normal` / `high` via `NtSetInformationProcess` after job assignment |

`CPUWeight=` and `CPUQuota=` cannot both be set. Job Objects have no I/O-priority class; a failed `IoPriority=` set fails activation with reason `configuration`. These are Windows Job Object semantics, not cgroup `cpu.weight` / `cpu.max` / `io.weight`.

`winctl verify` rejects bad sizes, `ProcessLimit=0` or negative, unknown `PriorityClass=` (including `realtime`), `CPUWeight=` outside 1–10000, `CPUQuota=` without `%` or N outside 1–100, both CPU keys together, unknown `IoPriority=`, and those keys on `.timer` / `.target`.

### Proxy unit types

**`Type=scm`** — system-manager proxy for an existing SCM service (`ServiceName=` required, e.g. `MSSQLSERVER`). Maps StartService / StopService / QueryServiceStatusEx to ActiveState. Already running/stopped is success. No CreateProcess, Job Object, ExecStart, notify pipe, `WINUNIT_*` env, or WatchdogMode. Timeouts wait for SCM state. `Restart=` applies to start failure only; winunitd does not watch the service after it reports running. Invalid in a user unit. Does not register or edit SCM configuration (not `import-service`).

**`Type=scheduled-task`** — system-manager proxy for an existing Task Scheduler task (`TaskName=` required, e.g. `\Backups\LegacyBackup`). Native `.timer` is preferred for new schedules; this type is legacy interop. Maps IRegisteredTask.Run / Stop / State to ActiveState. Same constraints as `Type=scm` (no process ownership, no watchdog; `Restart=` on start failure only). Invalid in a user unit. Does not create or edit the task definition (not `import-task`).

After=/Requires=/Wants= work so a native unit can wait until the proxy reports running.

### Companion units

Companions start `foo.service` by basename. `Unit=` is not accepted. The companion `.service` must sit next to the companion file.

| Type | Trigger | Notes |
| --- | --- | --- |
| `.registry` | `[Registry]` `RegistryChanged=HKLM\…` or `HKCU\…` | Key+subtree via `RegNotifyChangeKeyValue`. Missing key → reason `configuration`. System-scope verify rejects `HKCU`; `winctl --user verify` accepts `HKCU` and `HKLM`. |
| `.eventlog` | `[EventLog]` `EventLogTrigger=<Channel>:EventID=<uint16>` | Via `EvtSubscribe`. Subscribe failure or unknown channel → reason `configuration`. `winctl --user verify` rejects `System` and `Security`. |
| `.path` | `[Path]` `PathChanged=` (absolute, repeatable **OR**) and/or `PathExists=` (absolute, repeatable **AND** — lock vs systemd OR) | Mix: either may start. `ReadDirectoryChangesW` non-recursive; a file path watches the parent and filters by name. Bad `PathChanged=` → reason `configuration`; missing `PathExists=` waits for creation. |

### Timers

Internal scheduler (not Task Scheduler). `OnBootSec` is since machine boot; `OnStartupSec` is since this winunitd instance. A `foo.timer` activates `foo.service` when `Unit=` is omitted.

`winctl list-timers` / `status` show next/last elapse when a timer is active. Calendar `Next` is recomputed after a wall-clock jump (`Engine.ClockChanged`, driven by SCM `SERVICE_CONTROL_TIMECHANGE` and `SERVICE_CONTROL_POWEREVENT` / `PBT_APMRESUMEAUTOMATIC`). Console mode has no `WM_TIMECHANGE` window; a 30s poll is the fallback. Timer last-run state lives under `<base-dir>\runtime\timers\`.

## Journal and logs

stdout/stderr are stored under `<base-dir>\journal\<encoded-unit>.log` (system: `C:\ProgramData\winunitd\journal\`; user: `%LOCALAPPDATA%\winunitd\journal\`). Current file capped at 10 MiB with 3 rotated generations (`.log.1` … `.log.3`). Filenames percent-encode Windows-forbidden characters. The writer timer-flushes; `Sync` runs on unit exit, daemon shutdown, and rotate — not per line.

Records are JSON lines with `v=3` (`continuation`, `partial`, `severity`, `session`, `userSid`, plus timestamp/unit/pid/stream/message/invocationId). Captured stdout/stderr messages are split at UTF-8 boundaries into fragments of at most 64 KiB; JSON escaping can expand the stored record. `continuation=true` joins the preceding fragment for the same invocation and stream; `partial=true` means the fragment has no terminating newline, including an unterminated final line. These fields are also exposed by the logs API. Severity from stream: stdout→`info`, stderr→`err`. Readers accept mixed `v=1`/`v=2`/`v=3` and do not rewrite old lines. Older readers may display fragments as separate lines.

Capture uses a bounded writer queue: at most 4 MiB of pending message data per invocation, 16 MiB per store, 12,288 pending fragments per invocation, and 16,384 queued fragments overall. The per-invocation record cap prevents one stream of short lines from consuming the entire shared queue. These limits exclude stream read buffers, per-unit flush buffers, record metadata, JSON expansion, and other manager memory. The single writer preserves enqueue order; blocked storage can affect every unit's journal, but capture keeps draining and rejects new fragments when a limit is reached.

Unit status exposes cumulative `logDroppedRecords`, `logDroppedBytes` (normalized message bytes, excluding line endings), `logStorageErrors`, and `logLastStorageError`. Counters reset with the manager. Drop counters cover rejected fragments and pending records abandoned by a failed close. Sync failures or external file damage can lose additional data. Do not treat continuation metadata as proof of a complete line after loss.

Journal waits honor their context during capture and sync; close waits up to five seconds, returns an error on timeout, and retains file ownership for cleanup if storage recovers. After an interrupted append, reopening the journal adds a missing newline before new records and reports the repair; existing bytes are preserved. During a live write failure, each file retains the exact unwritten suffix of its current batch (at most 4 KiB or one larger serialized record) and retries with a 1 to 30 second backoff. New records for that file are rejected and counted while recovery is pending. A recovered write resumes without reopening or duplicating the retained record. Actual volume exhaustion and automatic recovery passed on a disposable Server Core baseline copy; see [R1 evidence](R1-EVIDENCE.md). Fairness under aggregate overload remains pending.

```text
winctl logs UNIT [--follow] [--since <when>]
```

`--since` is a lower bound (RFC3339, `YYYY-MM-DD`, Go duration such as `1h`, or `1 hour ago`); a bad value is an error. `--follow` polls new lines with a cursor over the existing `logs` RPC (no streaming). `--boot` is not implemented.

Log responses are paginated below the 1 MiB RPC limit. `winctl logs` reads every page, including without `--follow`. API clients should request the returned `cursor` while `more` is true. A single entry too large for a response returns an explicit error. Each query has a five-second deadline, and at most four queries per store may occupy storage workers. A timed-out native read or flush retains its slot until it returns; further queries receive a capacity error when all four slots are occupied. Cancellation preserves the supplied cursor. These limits bound requests and workers; they cannot interrupt an underlying Windows filesystem call.

## User managers and linger

Interactive user managers require admission. The default admits only administrator-enabled users in `<base-dir>\user-admission.json`; a missing file admits none. Administrators may instead select `unit-files` mode to delegate admission to user unit-file presence. Explicit per-SID disable overrides delegation. See [configuration and qualification limits](USER-ADMISSION.md).

For an admitted interactive user the system manager launches `winunitd --user-manager <SID>` (same binary, not an extra SCM service) using `WTSQueryUserToken`. One manager per SID. System `list-units` does not show user units. Policy and existing sessions are reconciled every ten seconds; explicit changes also invalidate pending admission results.

User units load from `%LOCALAPPDATA%\winunitd\units\`. User processes get a deterministic environment (`USERPROFILE`, `LOCALAPPDATA`, `APPDATA`, `TEMP`, `TMP`, `USERNAME`, `USERDOMAIN`).

Each user manager watches WTS for its SID and starts builtin `graphical-session.target` while that SID has a suitable interactive session (Inactive when none, including linger-without-session). `RequiresInteractiveSession=yes` skips that unit when no suitable interactive session exists (SessionMode is not implemented).

Administrators can `winctl enable-linger <user>` on the system pipe (not `--user`). Linger state is a tiny record at `<base-dir>\linger\<SID>` (not an NTFS symlink). At boot, lingering user managers start with no session. Last logoff does not kill a lingering manager; `disable-linger` kills it if no session remains. Non-admin enable-linger fails closed.

S4U uses a trusted LSA connection (`LsaRegisterLogonProcess`; LocalSystem has `SeTcbPrivilege`). An optional named CredMan/LSA URI on the linger record is used only if present and S4U is insufficient for outbound network creds. That fallback calls `LogonUserW` with `LOGON32_LOGON_BATCH` (never a password in a unit file, env, or path). The daemon logs which path produced the token (`s4u` vs `store-uri`).

## Unverified

Unit tests and GitHub Actions `go test ./...` on `windows-latest` are not a live production Windows proof. These paths are implemented; they have not been signed off on a real machine in the conditions below. Silence is not verification.

- **Job CPU/I/O** — `CPUWeight=` / `CPUQuota=` and `IoPriority=` are unit-tested (set + query back). Live throttling and I/O-class effect under load are unverified.
- **EvtSubscribe** — `.eventlog` subscribe + Application `ReportEvent` tests exist. Production channel rights, Security, and custom logs on a real host are unverified.
- **ReadDirectoryChangesW** — `.path` watches fire in unit tests on temp directories. Long-lived, network, reparse-point, and volume-unmount behavior is unverified.
- **Lingering-child journal** — journal wait is bounded by `TimeoutStopSec` (#83). That linger-and-abandon path on a live Windows host is unverified.
- **Admin vs non-admin pipe Peer / elevated `winctl --user`** — token Peer and DACL tests exist. UAC-filtered vs elevated token on the user pipe is unverified. CI runners are typically already Administrators.
- **Live SYSTEM S4U** — `TestSYSTEMLingerTokenDuplicatePrimaryAndCreateProcessAsUser` skips unless LocalSystem (`psexec -s`). GitHub Actions `windows-latest` is not SYSTEM.
- **SCM TIMECHANGE / POWEREVENT** — the service accepts those controls. Live delivery to a running service (calendar `Next` recompute) is unverified. Console mode falls back to a 30s poll.

Development builds support `winctl cancel ID`; see [explicit cancellation](OPERATIONS.md#explicit-cancellation) for completed-member preservation, pending cleanup and exit codes.
