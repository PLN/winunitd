# winunitd

Declarative Windows unit manager and process supervisor.

Design notes live in [DESIGN.md](DESIGN.md) and are the source of truth.

## Binaries

| Binary | Role |
| --- | --- |
| `winunitd.exe` | Daemon: loads units and supervises processes |
| `winctl.exe` | Control CLI for the daemon |
| `winunit-notify.exe` | Helper for units to report ready / status |

Windows is the first-class target (`GOOS=windows`).

## Status

**Now:** unit file loader, `winctl verify` on a file path (no daemon required), a dependency graph with start transactions, a versioned JSON-RPC control API on `\\.\pipe\winunitd\control` (LocalSystem and Administrators), an SCM host with a daemon-level Job Object for strict ownership, per-unit Job Objects with real CreateProcess, `Restart=` (`no` / `always` / `on-failure` / `on-watchdog`), `[Unit]` `StartLimitIntervalSec=` / `StartLimitBurst=` (defaults 10s / 5; `StartLimitBurst=0` unlimited; burst starts inside the interval fail with reason `start-limit`; explicit `winctl start` resets), enable/targets at boot, a journal store under `<base-dir>\journal\`, an internal timer scheduler (not Task Scheduler), ordered stop on SCM stop / preshutdown / console SIGINT, logged-on per-user managers, lingering, session tracking (`graphical-session.target`), `Type=notify` readiness, `WatchdogSec=` notify heartbeats, `WatchdogMode=tcp` / `http` health checks, `winunit-notify.exe`, per-start invocation IDs (`InvocationID=` on `winctl status`, journal lines, `WINUNIT_INVOCATION_ID`), `Type=scm` SCM proxy units (`ServiceName=` required; `winctl start`/`stop`/`status` map to StartService / StopService / QueryServiceStatusEx), `Type=scheduled-task` Task Scheduler proxy units (`TaskName=` required; `winctl start`/`stop`/`status` map to IRegisteredTask.Run / Stop / State), R1 Job Object limits on the existing per-unit job (`MemoryMax=` job-wide commit cap with `K`/`M`/`G`, `ProcessLimit=` active processes, `PriorityClass=` `idle`/`below-normal`/`normal`/`above-normal`/`high` — not `realtime`), T1 `.registry` companion units (`[Registry]` `RegistryChanged=HKLM\…` or `HKCU\…`; key+subtree via `RegNotifyChangeKeyValue`; `foo.registry` starts `foo.service` by basename; no `Unit=`; missing key fails the registry unit with reason `configuration`), T2 `.eventlog` companion units (`[EventLog]` `EventLogTrigger=<Channel>:EventID=<uint16>` via `EvtSubscribe`; `foo.eventlog` starts `foo.service` by basename; no `Unit=`; subscribe failure or unknown channel fails the event log unit with reason `configuration`), and P-Path `.path` companion units (`[Path]` `PathChanged=` absolute Windows paths, repeatable OR, and `PathExists=` absolute Windows paths, repeatable AND — a lock versus systemd OR; mix: either may start; `ReadDirectoryChangesW` non-recursive; a file path watches the parent directory and filters by name; `foo.path` starts `foo.service` by basename; no `Unit=`; missing or unwatchable `PathChanged=` fails the path unit with reason `configuration`; missing `PathExists=` waits for creation). Hitting `MemoryMax=` or `ProcessLimit=` fails the unit with reason `resource-limit` on `winctl status`; `Restart=` still applies. `CPUWeight=`, `CPUQuota=`, `IoPriority=`, `RestartMaxDelaySec=`, and `RestartBackoff=` are not parsed. `winctl enable` writes files under `<base-dir>\enabled\<target>\<unit>` (not NTFS symlinks). A fresh daemon start (SCM or console) starts `default.target`, which Wants=`timers.target` so enabled timers arm, and pulls in enabled Wants=/Requires= only. `OnBootSec` is since machine boot; `OnStartupSec` is since this winunitd instance. `winctl daemon-reload` reparses units and rebuilds the graph without dropping live jobs. `winctl list-timers` / `status` show next/last elapse when a timer is active. Calendar `Next` is recomputed after a wall-clock jump (`Engine.ClockChanged`, driven by SCM `SERVICE_CONTROL_TIMECHANGE` and `SERVICE_CONTROL_POWEREVENT` / `PBT_APMRESUMEAUTOMATIC` when running as a service). Console mode has no `WM_TIMECHANGE` window; a 30s poll is the fallback. Manager shutdown stops units in reverse After=/Before= order (`shutdown.target` as the stop root) and then closes the daemon Job Object.

On first interactive logon the system manager launches `winunitd --user-manager <SID>` (same binary, not an extra SCM service) using `WTSQueryUserToken`. Administrators can `winctl enable-linger <user>` on the system pipe (not `--user`); linger state is a tiny record at `<base-dir>\linger\<SID>` (not an NTFS symlink). At boot, lingering user managers start with no session. S4U uses a trusted LSA connection (`LsaRegisterLogonProcess`; LocalSystem has `SeTcbPrivilege`). An optional named CredMan/LSA URI on the linger record is used only if it is present and S4U is insufficient for outbound network creds (logon-session probe, not a hardcoded miss). That fallback calls `LogonUserW` with `LOGON32_LOGON_BATCH` so the token can carry outbound network credentials (`LOGON32_LOGON_NETWORK` does not cache them). Never a password in a unit file, env, or path. The daemon logs which path produced the token (`s4u` vs `store-uri`). Last logoff does not kill a lingering manager; `disable-linger` kills it if no session remains, otherwise P1 last-logoff still applies. Non-admin enable-linger fails closed. `RequiresInteractiveSession=yes` skips that unit when no suitable interactive session exists (SessionMode is not implemented). Each user manager watches WTS for its SID and starts builtin `graphical-session.target` while that SID has a suitable interactive session (Inactive when none, including linger-without-session). User units load from `%LOCALAPPDATA%\winunitd\units\` and are controlled with `winctl --user` on `\\.\pipe\winunitd\user\<SID>\control`. One manager per SID. System `list-units` does not show user units. User processes get a deterministic environment (`USERPROFILE`, `LOCALAPPDATA`, `APPDATA`, `TEMP`, `TMP`, `USERNAME`, `USERDOMAIN`).

Each unit start (including `Restart=` relaunch) gets a new UUID invocation ID. `winctl status` prints `InvocationID=`. Journal lines for that run carry the same ID. The process env includes `WINUNIT_INVOCATION_ID`. The notify pipe name stays `\\.\pipe\winunitd\notify\<unit>`.

`Type=notify` stays activating until the unit writes `READY=1` on `WINUNIT_NOTIFY_PIPE` (`\\.\pipe\winunitd\notify\<unit>`), or `TimeoutStartSec` fires. `WatchdogSec=` defaults to `WatchdogMode=notify` and injects `WINUNIT_WATCHDOG_USEC`; a missed heartbeat fails the unit (then `Restart=`). `WatchdogMode=tcp` is connect-only on `WatchdogEndpoint=` (`127.0.0.1:port` or `[::1]:port`). `WatchdogMode=http` GETs `WatchdogEndpoint=` (`http://127.0.0.1:...`) and expects `WatchdogExpectedStatus=` (default 200). TCP/HTTP endpoints must be loopback; non-loopback is a parse error and is never dialed. A missed or failed probe uses the same fail path as notify (`failed` / `watchdog`, then `Restart=` including `on-watchdog`). `NotifyAccess=main` only. Scripts send `READY` / `WATCHDOG` / `STATUS` with `winunit-notify.exe`.

`Type=scm` is a system-manager proxy for an existing SCM service (`ServiceName=` required, e.g. `MSSQLSERVER`). Start/stop/status call StartService / ControlService STOP / QueryServiceStatusEx and map to ActiveState (`active` / `inactive` / `activating` / `deactivating` / `failed`). Already running is a successful start; already stopped is a successful stop. There is no CreateProcess, Job Object, ExecStart, notify pipe, `WINUNIT_*` environment, or WatchdogMode. `TimeoutStartSec` / `TimeoutStopSec` wait for the SCM state, then fail. `Restart=` applies to SCM start failure the same way as a process start failure; winunitd does not watch the service after it reports running (that would fight SCM recovery). `Type=scm` in a user unit is a load/start error. After=/Requires=/Wants= work so a native unit can wait until the proxy reports running. This does not register, change, or delete SCM configuration, and it is not `import-service`.

`Type=scheduled-task` is a system-manager proxy for an existing Task Scheduler task (`TaskName=` required, e.g. `\Backups\LegacyBackup`). Native `.timer` remains the recommended path for new schedules; this type is interop for legacy tasks only. Start/stop/status call IRegisteredTask.Run / Stop / State (plus running instances) and map to ActiveState (`active` / `activating` / `inactive` / `failed`). Already running is a successful start; already stopped is a successful stop. There is no CreateProcess, Job Object, ExecStart, notify pipe, `WINUNIT_*` environment, or WatchdogMode. `TimeoutStartSec` / `TimeoutStopSec` wait for the corresponding task state, then fail. `Restart=` applies to start failure (failed Run) the same way as a process start failure; winunitd does not supervise the instance after it reports running (Task Scheduler owns it). `Type=scheduled-task` in a user unit is a load/start error. After=/Requires=/Wants= work so a native unit can wait until the proxy reports running. This does not create, edit, delete, or enable/disable the task definition, and it is not `import-task`.

**Later:** ExecStop / CTRL_BREAK / WM_CLOSE, GUI attach, SessionPolicy, LoadCredential, WatchdogMode=window — as described in DESIGN.md.

## Build

```text
go test ./...
go build -o winunitd.exe ./cmd/winunitd
go build -o winctl.exe ./cmd/winctl
go build -o winunit-notify.exe ./cmd/winunit-notify
```

## Windows Service

```text
winunitd install [--base-dir DIR]
winunitd uninstall
```

Install registers `winunitd` (DisplayName `WinUnit Manager`) as LocalSystem, Automatic (Delayed Start), with SCM recovery set to restart on failure, and accepts preshutdown notification (used for ordered stop). Install creates `C:\ProgramData\winunitd` (`units\`, `enabled\`, `journal\`, `runtime\`, `linger\`) if missing. It does not add PATH or register an Event Log provider. The daemon creates a Job Object with `KILL_ON_JOB_CLOSE`: if `winunitd.exe` is killed, assigned child processes die with it. Each started unit gets its own nested Job Object (no breakaway). Console mode (`winunitd --base-dir DIR`) still works without SCM.

On start (SCM or console) the daemon starts `default.target`. Built-in targets are `default.target`, `timers.target`, and `shutdown.target` (`network-online.target` is not shipped). A user manager also loads `graphical-session.target` (Active while that SID has a suitable interactive session). Builtin `default.target` Wants= `timers.target` so enabled timers run after boot. On `sc stop`, preshutdown, or console SIGINT, units stop in reverse After=/Before= order, then the daemon Job Object is closed. `winunitd uninstall` requests that ordered stop, then unregisters the service. stdout/stderr are stored under `<base-dir>\journal\<unit>.log` (production `C:\ProgramData\winunitd\journal\`). Timer last-run state lives under `<base-dir>\runtime\timers\`.

The system manager is the only SCM service. On first interactive logon it starts `winunitd --user-manager <SID>` under a per-user Job Object. Lingering users are started at boot without a session. User units and journals live under `%LOCALAPPDATA%\winunitd\`. `winctl enable-linger` / `disable-linger` talk to the system pipe and require Administrators.

## Verify unit files

`winctl verify` parses a systemd-like INI unit file and checks MVP rules. It does not need a running daemon:

```text
winctl verify C:\path\foo.service
winctl verify C:\path\foo.timer
winctl verify C:\path\foo.registry
winctl verify C:\path\foo.eventlog
winctl verify C:\path\foo.path
```

Unknown directives are errors. `ExecStart=` must be an absolute Windows path (`SearchPath=no`). An omitted `WorkingDirectory=` is a warning; System32 is not used as a default. `Environment=` values are literals (`${}` is not expanded). A `foo.timer` activates `foo.service` when `Unit=` is omitted. A `foo.registry` activates `foo.service` by basename (`Unit=` is not accepted). `RegistryChanged=` is `HKLM\…` or `HKCU\…` only (no PowerShell drive). System-scope verify rejects `HKCU`; `winctl --user verify` accepts `HKCU` and `HKLM`. The companion `.service` must sit next to the `.registry` file. A `foo.eventlog` activates `foo.service` by basename (`Unit=` is not accepted). `EventLogTrigger=` is `<Channel>:EventID=<uint16>` only. `winctl --user verify` rejects `System` and `Security`; Application and custom names are allowed. The companion `.service` must sit next to the `.eventlog` file. A `foo.path` activates `foo.service` by basename (`Unit=` is not accepted). `PathChanged=` is an absolute Windows path (repeatable is OR). `PathExists=` is an absolute Windows path (repeatable is AND; a lock versus systemd OR). Mixing both: either may start. Watches are non-recursive (`ReadDirectoryChangesW`); a file path watches the parent directory and filters by name. The companion `.service` must sit next to the `.path` file. `Type=scm` requires `ServiceName=` and does not use `ExecStart=`. `Type=scheduled-task` requires `TaskName=` and rejects `ExecStart=` / `ExecStartArg=`. `MemoryMax=`, `ProcessLimit=`, and `PriorityClass=` are `[Service]` only; `winctl verify` rejects bad sizes, `ProcessLimit=0` or negative, unknown `PriorityClass=` (including `realtime`), and those keys on `.timer` / `.target`.

`winctl verify foo.service` (a unit name, not a path) talks to the daemon. Other `winctl` commands (`start`, `stop`, `restart`, `status`, `enable`, `disable`, `list-units`, `list-timers`, `logs`, `daemon-reload`, `enable-linger`, `disable-linger`) always use the control pipe. `winctl --user …` uses `\\.\pipe\winunitd\user\<SID>\control` for the current user; bare `winctl` stays on the system pipe. Linger admin verbs do not use `--user`.
