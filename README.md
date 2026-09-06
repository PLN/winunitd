# winunitd

Run Windows background applications from declarative unit files. winunitd starts
processes in dependency order, owns their process trees, captures output, and
restarts workloads when they fail.

It borrows unit files and familiar commands from systemd, using Windows services,
Job Objects, and named pipes underneath. It is not a systemd compatibility layer.
See the [unit reference](docs/UNIT-REFERENCE.md) for the supported syntax.

**[Download 0.2.1-beta](https://github.com/PLN/winunitd/releases/tag/v0.2.1-beta)**
for Windows 11 Enterprise/LTSC x64. Qualified on Enterprise LTSC build 26100.9168.
The MSI is unsigned. No Go or .NET runtime is needed on the destination.

## Install and try it

Run the MSI from an elevated PowerShell terminal in your download directory:

```powershell
msiexec.exe /i .\winunitd-0.2.1-x64-beta.msi /qb
```

Wait for installation to finish. It starts the LocalSystem `winunitd` service and
installs the commands under `C:\Program Files\winunitd`. PATH is not changed.
System units run with SYSTEM privileges; keep their files administrator-controlled.
No example workload is enabled automatically.

Try the included worker, which prints a line every five seconds:

```powershell
$install = Join-Path $env:ProgramFiles 'winunitd'
$ctl = Join-Path $install 'winctl.exe'
$units = Join-Path $env:ProgramData 'winunitd\units'

& $ctl verify --file "$install\examples\worker.service" "$install\examples\worker.target"
Copy-Item "$install\examples\worker.service", "$install\examples\worker.target" $units
& $ctl daemon-reload
& $ctl start worker.target
& $ctl status worker.service
& $ctl logs worker.service
```

The example assumes Windows is installed at `C:\Windows`. Allow a few seconds
for the worker's first output. To restart the group or stop it:

```powershell
& $ctl restart worker.target
& $ctl stop worker.target
```

Use `& $ctl enable worker.service` to start the worker whenever the daemon starts.
`disable` removes that future activation; it does not stop a running workload.
Unit files live under `C:\ProgramData\winunitd\units`; logs and enablement are
stored alongside them. See the [examples](examples/beta/README.md) and
[unit reference](docs/UNIT-REFERENCE.md) to configure your own applications.

## What this beta supports

The supported path is a system service managing `Type=simple` services and
`.target` groups: dependencies, start/stop/restart, logging, configuration reload,
explicit enablement, and workload restart policies. The documented core unit
syntax is preserved across beta updates.

Install, repair, upgrade, failed-upgrade rollback, reboot, uninstall/reinstall,
and workload recovery passed on a disposable LTSC guest. The same binaries also
passed an existing Hermes pilot's maintenance rehearsal and response check.
See [qualification details](docs/BETA-QUALIFICATION.md).

The main limits are:

- Stop terminates the owned process job. Graceful application stop hooks are not
  implemented.
- User managers and linger, notify/watchdogs, timers, resource limits, native
  event triggers, and SCM/task proxies are experimental. Interactive user
  admission is disabled by default.
- The MSI uses ordinary automatic service startup. It does not configure
  automatic recovery after a daemon failure.

See [runtime behavior and advanced features](docs/RUNTIME-REFERENCE.md) for
operational details and [user admission](docs/USER-ADMISSION.md) for that policy.

## Upgrade, repair, and uninstall

Back up `C:\ProgramData\winunitd` and keep the previous MSI. Before changing an
installation, stop the service and wait for it to reach Stopped:

```powershell
Stop-Service winunitd
(Get-Service winunitd).WaitForStatus('Stopped', [TimeSpan]::FromMinutes(3))
```

Close commands using the installed files, then run the new MSI to upgrade.
The installer rejects a running or transitioning service. Successful install,
repair, and upgrade start the service again. Uninstall retains configuration
and logs; reinstall reuses them.

Follow the [installation and recovery instructions](packaging/beta/INSTALL.md)
for repair, uninstall, failed upgrades, or migration from a manual installation.
Do not run `winunitd install` or `winunitd uninstall` against an MSI installation.

## Build and contribute

The project is written in Go and licensed under [MIT](LICENSE). The binaries are:

| Binary | Purpose |
| --- | --- |
| `winunitd.exe` | Loads units and supervises processes |
| `winctl.exe` | Controls the manager and reads status/logs |
| `winunit-notify.exe` | Reports readiness, status, and watchdog heartbeats |

Use the Go version in [.go-version](.go-version). From a Windows checkout:

```powershell
go test ./...
go run ./tools/build -out dist
```

Build an MSI from clean source with
`./packaging/beta/build.ps1 -PackageVersion 0.2.1`.
[BUILDING.md](docs/BUILDING.md) covers the pinned toolchain, manifests, CI, and
dependency maintenance. Unstamped development binaries retain the `0.1.0-alpha`
fallback label; the manifest records their exact source revision.

The [roadmap](ROADMAP.md), [design](DESIGN.md), and
[milestones](docs/MILESTONES.md) describe future work. Proposed design semantics
are not the current unit-file contract. The [architecture review](docs/DESIGN-REVIEW.md)
and [release notes](docs/RELEASE-NOTES-0.2.1-beta.md) provide further background.
