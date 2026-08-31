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

**Now:** unit file loader, `winctl verify` on a file path (no daemon required), a dependency graph with start transactions, a versioned JSON-RPC control API on `\\.\pipe\winunitd\control` (LocalSystem and Administrators), an SCM host with a daemon-level Job Object for strict ownership, per-unit Job Objects with real CreateProcess, `Restart=` (`no` / `always` / `on-failure`), and enable/targets at boot. `winctl enable` writes files under `<base-dir>\enabled\<target>\<unit>` (not NTFS symlinks). A fresh daemon start (SCM or console) starts `default.target`, which pulls in enabled Wants=/Requires= only. `winctl daemon-reload` reparses units and rebuilds the graph without dropping live jobs.

**Later:** journal store, timers at runtime, and ordered `sc stop` — as described in DESIGN.md.

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

Install registers `winunitd` (DisplayName `WinUnit Manager`) as LocalSystem, Automatic (Delayed Start), with SCM recovery set to restart on failure, and accepts preshutdown notification. The daemon creates a Job Object with `KILL_ON_JOB_CLOSE`: if `winunitd.exe` is killed, assigned child processes die with it. Each started unit gets its own nested Job Object (no breakaway). Console mode (`winunitd --base-dir DIR`) still works without SCM.

On start (SCM or console) the daemon starts `default.target`. Built-in targets are `default.target`, `timers.target`, and `shutdown.target` (`network-online.target` is not shipped). Ordered stop on `sc stop` is not implemented yet. The journal store and timer runtime are not implemented yet.

## Verify unit files

`winctl verify` parses a systemd-like INI unit file and checks MVP rules. It does not need a running daemon:

```text
winctl verify C:\path\foo.service
winctl verify C:\path\foo.timer
```

Unknown directives are errors. `ExecStart=` must be an absolute Windows path (`SearchPath=no`). An omitted `WorkingDirectory=` is a warning; System32 is not used as a default. `Environment=` values are literals (`${}` is not expanded). A `foo.timer` activates `foo.service` when `Unit=` is omitted.

`winctl verify foo.service` (a unit name, not a path) talks to the daemon. Other `winctl` commands (`start`, `stop`, `restart`, `status`, `enable`, `disable`, `list-units`, `list-timers`, `logs`, `daemon-reload`) always use the control pipe.
