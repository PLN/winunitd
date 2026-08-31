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

**Now:** unit file loader, `winctl verify` (no daemon required), and a dependency graph with start transactions (Requires/Wants vs After/Before). Still no daemon.

**Later:** process supervision (Job Objects, tokens, sessions), timers at runtime, journal, and a named-pipe control API — as described in DESIGN.md.

## Build

```text
go test ./...
go build -o winunitd.exe ./cmd/winunitd
go build -o winctl.exe ./cmd/winctl
go build -o winunit-notify.exe ./cmd/winunit-notify
```

## Verify unit files

`winctl verify` parses a systemd-like INI unit file and checks MVP rules. It does not need a running daemon:

```text
winctl verify C:\path\foo.service
winctl verify C:\path\foo.timer
```

Unknown directives are errors. `ExecStart=` must be an absolute Windows path (`SearchPath=no`). An omitted `WorkingDirectory=` is a warning; System32 is not used as a default. `Environment=` values are literals (`${}` is not expanded). A `foo.timer` activates `foo.service` when `Unit=` is omitted.
