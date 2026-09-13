# winunitd beta installation

Candidate package; qualification results must accompany a release. Supported
qualification targets are Windows 11 Enterprise/LTSC x64. No Go, .NET, or WiX is
needed on the destination. This beta package is unsigned.

Install from an elevated terminal with `msiexec /i PACKAGE.msi /qb`, or use
`/qn /norestart /l*v install.log` for unattended installation. It installs into
Program Files\winunitd and starts the LocalSystem `winunitd` service. Commands
are available by full path; PATH is not changed. No example workload is enabled.

Place administrator-controlled units under ProgramData\winunitd\units. Use
`winctl verify`, `daemon-reload`, `enable`, and `start` as documented in the unit
reference. System units run with SYSTEM privileges. Interactive user admission
defaults to disabled; advanced user/linger modes are experimental.

Before repair, upgrade, or uninstall, run `Stop-Service winunitd` and wait for
the service to reach Stopped. The package rejects a running or transitioning
service. Close winctl/log-follow sessions and any other process using installed
files. Do not restart the service until msiexec completes. Ordinary repair uses
`msiexec /fa PACKAGE.msi`; upgrade uses `/i NEW_PACKAGE.msi`; uninstall uses
`/x PACKAGE.msi`. Successful install/repair/upgrade starts the service.

Configuration, enablement, journals, and timer state are retained on uninstall;
reinstall reuses them. Back up ProgramData\winunitd before upgrades. A failed
upgrade should restore the prior package; verify the old binaries and explicitly
start the service after inspecting the failure. Keep the old MSI for recovery.
Do not run `winunitd install`/`uninstall` against an MSI-owned installation.

An existing manually registered service is a migration conflict. Back up its
configuration and registration, stop it, remove its registration using the old
installation, then install the MSI. Existing product directories must already be
owned and writable only by administrators/SYSTEM; redirected directories are
rejected. There is no automatic migration of personal application data or tasks.

Manual installation and MSI install/repair/upgrade select ordinary automatic SCM
startup, three one-second restart actions with no failure-count reset, recovery
on non-crash failures, and a three-minute preshutdown timeout. Automatic startup
does not promise a fixed boot deadline. Workload `Restart=` is a separate policy.
MSI rollback restores the previous startup/recovery settings after restoring the
registration. Each transaction uses a new random identity and exclusive protected
rollback state; failed cleanup retains that state for inspection. Automated
maintenance remains follow-up work. See the unit reference for stop behavior.
