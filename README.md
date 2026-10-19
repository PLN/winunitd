# winunitd

Run Windows background applications from declarative unit files. winunitd starts
processes in dependency order, owns their process trees, captures output, and
restarts workloads when they fail.

It borrows unit files and familiar commands from systemd, using Windows services,
Job Objects, and named pipes underneath. It is not a systemd compatibility layer.
See the [unit reference](docs/UNIT-REFERENCE.md) for the supported syntax.

**[Download 0.5.1](https://github.com/PLN/winunitd/releases/tag/v0.5.1)**
for Windows 11 Enterprise and Enterprise LTSC x64. No Go or .NET runtime is needed
on the destination. The MSI is not code-signed yet; check it against the release's
`SHA256SUMS` and its build attestation as the
[release notes](docs/RELEASE-NOTES-0.5.1.md) describe.

## Install and try it

Run the MSI from an elevated PowerShell terminal in your download directory:

```powershell
msiexec.exe /i .\winunitd-0.5.1-x64.msi /qb
```

It starts the LocalSystem `winunitd` service, installs the commands under
`C:\Program Files\winunitd\bin` and adds that folder to PATH. Open a new elevated
terminal so that `winctl` is found. System units run with SYSTEM privileges; keep
their files administrator-controlled. No example workload is enabled.

Try the included worker, which prints a line every five seconds:

```powershell
$examples = Join-Path $env:ProgramFiles 'winunitd\doc\examples'
$units = Join-Path $env:ProgramData 'winunitd\units'

winctl verify --file "$examples\worker.service" "$examples\worker.target"
Copy-Item "$examples\worker.service", "$examples\worker.target" $units
winctl daemon-reload
winctl start worker.target
winctl status worker.service
winctl logs worker.service
```

The example assumes Windows is installed at `C:\Windows`. Allow a few seconds
for the worker's first output. To restart the group or stop it:

```powershell
winctl restart worker.target
winctl stop worker.target
```

Use `winctl enable worker.service` to start the worker whenever the daemon starts.
`disable` removes that future activation; it does not stop a running workload.
Unit files live under `C:\ProgramData\winunitd\units`; logs and enablement are
stored alongside them. See the [unit reference](docs/UNIT-REFERENCE.md) to
configure your own applications.

## What it supports

The system manager runs services, oneshots and targets with dependencies,
start/stop/restart including a cooperative stop command, captured logs,
configuration reload, explicit enablement, restart policies with backoff,
readiness and health probes, and timers. Admitted users get their own manager
for units under `%LOCALAPPDATA%\winunitd`, and `winctl enable-linger` keeps a
user's manager running without a sign-in, for local and Active Directory domain
accounts. Go programs can drive the managers through the
[`client`](client/doc.go) package.

[SUPPORT.md](SUPPORT.md) lists the supported configurations and the limits, and
[runtime behavior](docs/RUNTIME-REFERENCE.md), [operations](docs/OPERATIONS.md)
and [user admission](docs/USER-ADMISSION.md) cover the details.

## Upgrade, repair, and uninstall

Run a newer MSI to upgrade. The installer asks the running manager to quiesce,
stops the service, replaces the files and starts it again; if that fails it
restores the previous files and service state. Enabled units start again after
an upgrade. Uninstall keeps `C:\ProgramData\winunitd`, and a reinstall reuses it.
Downgrades are refused.

Coming from the unsigned 0.2.1-beta, remove the beta first; units, enablement and
logs carry over ([moving from the beta](docs/INSTALLATION.md#moving-from-the-unsigned-beta)).
The [installation guide](docs/INSTALLATION.md) covers repair, removal, exit codes
and the compatibility floor. Do not run `winunitd install` or
`winunitd uninstall` against an MSI installation.

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

Build the MSI from clean source with `./packaging/wix/build.ps1`. Published
releases come from the release workflow instead. [BUILDING.md](docs/BUILDING.md)
covers the pinned toolchain, manifests, CI, releases, and dependency maintenance.
Unstamped development binaries retain the `0.5.1` fallback label; the manifest
records their exact source revision.

For help and the supported configurations see [SUPPORT.md](SUPPORT.md); report
vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

The [roadmap](ROADMAP.md), [design](DESIGN.md), and
[milestones](docs/MILESTONES.md) describe future work. Proposed design semantics
are not the current unit-file contract. The [architecture review](docs/DESIGN-REVIEW.md)
and the release notes for [0.5.1](docs/RELEASE-NOTES-0.5.1.md),
[0.5.0](docs/RELEASE-NOTES-0.5.0.md),
[0.4.0](docs/RELEASE-NOTES-0.4.0.md) and [0.3.0](docs/RELEASE-NOTES-0.3.0.md)
provide further background.

This GitHub repository was recreated on October 14, 2026. The Git history, tags,
and releases carried over unchanged. Issue and pull request numbers up to #300
in older commit messages and documents refer to the original repository, whose
discussions are no longer public. Its open issues and draft pull request
continue here as #1 to #9.
