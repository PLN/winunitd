# winunitd 0.2.1-beta

First installable public-beta candidate for Windows 11 Enterprise/LTSC x64.
Qualified on Enterprise LTSC build 26100.9168. The package is unsigned.

Install the MSI from an elevated terminal. It installs the LocalSystem
`winunitd` service, three command-line binaries, licenses, unit reference, and
examples. No Go/.NET runtime is required; no sample workload is enabled.

The supported beta path covers simple services and targets, dependency ordering,
logging, configuration reload, explicit enablement, and workload restart policy.
The documented unversioned unit syntax is preserved across beta updates.

Before repair, upgrade, or uninstall, stop the service and wait for Stopped.
The installer enforces this requirement. Back up ProgramData\winunitd and keep
the previous MSI. Uninstall retains configuration and logs. A failed upgrade
requires inspecting the failure and explicitly restarting the restored service.

Stop currently terminates process jobs; graceful application stop hooks are not
implemented. PATH is not changed. Interactive admission defaults to disabled;
user/linger operation and advanced unit types remain experimental. Existing
manual installations require deliberate migration.

The candidate passed install, repair, failed-upgrade rollback, upgrade, reboot,
uninstall/reinstall, and workload recovery on a disposable LTSC guest. Its
binaries also passed an existing Hermes pilot's maintenance rehearsal and
post-recovery conversation check. See [qualification](BETA-QUALIFICATION.md)
and [installation instructions](../packaging/beta/INSTALL.md).

Candidate: `winunitd-0.2.1-x64-beta.msi` (15,888,384 bytes).
SHA256: `8ecabc2e40b03d81a3a683705969dd07b0685bb84ea544e92843d99902d6d141`.
Source: `1c56a57ae6001b247c1358a8c7395d734326fe1d`.

This is an unsigned prerelease for early adopters; see the qualification and
recovery instructions before using it for important workloads.
